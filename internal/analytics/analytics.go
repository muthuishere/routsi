// Package analytics asynchronously persists privacy-safe usage events to S3.
// It never stores prompts, responses, tool arguments, credentials, or raw
// conversation identifiers.
package analytics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	appconfig "github.com/muthuishere/routsi/internal/config"
)

type Event struct {
	SchemaVersion    int    `json:"schema_version"`
	Time             string `json:"time"`
	RequestedModel   string `json:"requested_model"`
	SelectedModel    string `json:"selected_model"`
	Provider         string `json:"provider"`
	Level            string `json:"level,omitempty"`
	Source           string `json:"source"`
	Status           int    `json:"status"`
	LatencyMs        int64  `json:"latency_ms"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalTokens      int    `json:"total_tokens"`
	TokenSource      string `json:"token_source"`
	Routed           bool   `json:"routed"`
	Escalated        bool   `json:"escalated"`
	ToolsRequested   bool   `json:"tools_requested"`
	ConversationHash string `json:"conversation_hash,omitempty"`
}

type Status struct {
	Enabled        bool   `json:"enabled"`
	Bucket         string `json:"bucket,omitempty"`
	Prefix         string `json:"prefix,omitempty"`
	Queued         int    `json:"queued"`
	Uploaded       int64  `json:"uploaded"`
	Dropped        int64  `json:"dropped"`
	SpooledBatches int64  `json:"spooled_batches"`
	UploadErrors   int64  `json:"upload_errors"`
}

type objectPutter interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

type Pipeline struct {
	cfg      appconfig.AnalyticsConfig
	put      objectPutter
	queue    chan Event
	stop     chan struct{}
	done     chan struct{}
	once     sync.Once
	uploaded atomic.Int64
	dropped  atomic.Int64
	spooled  atomic.Int64
	errors   atomic.Int64
}

func New(ctx context.Context, cfg appconfig.AnalyticsConfig) (*Pipeline, error) {
	cfg = cfg.Defaults()
	if cfg.Bucket == "" {
		return &Pipeline{cfg: cfg}, nil
	}
	var options []func(*awsconfig.LoadOptions) error
	if cfg.Region != "" {
		options = append(options, awsconfig.WithRegion(cfg.Region))
	}
	if cfg.Profile != "" {
		options = append(options, awsconfig.WithSharedConfigProfile(cfg.Profile))
	}
	accessKey, secretKey, sessionToken, err := analyticsCredentials(cfg)
	if err != nil {
		return nil, err
	}
	if accessKey != "" || secretKey != "" {
		options = append(options, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, sessionToken)))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("load analytics AWS configuration: %w", err)
	}
	if awsCfg.Region == "" {
		return nil, fmt.Errorf("analytics: AWS region is unavailable")
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.UsePathStyle = cfg.UsePathStyle
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
	})
	return newPipeline(cfg, client), nil
}

func analyticsCredentials(cfg appconfig.AnalyticsConfig) (string, string, string, error) {
	if (cfg.AccessKey != "" || cfg.SecretKey != "" || cfg.SessionToken != "") &&
		(cfg.AccessKeyEnv != "" || cfg.SecretKeyEnv != "" || cfg.SessionTokenEnv != "") {
		return "", "", "", fmt.Errorf("analytics: use either ${VAR} credential fields or *_env fields, not both")
	}
	if cfg.AccessKey != "" || cfg.SecretKey != "" {
		if cfg.AccessKey == "" || cfg.SecretKey == "" {
			return "", "", "", fmt.Errorf("analytics: access_key and secret_key must be set together")
		}
		return cfg.AccessKey, cfg.SecretKey, cfg.SessionToken, nil
	}
	if cfg.AccessKeyEnv != "" || cfg.SecretKeyEnv != "" {
		if cfg.AccessKeyEnv == "" || cfg.SecretKeyEnv == "" {
			return "", "", "", fmt.Errorf("analytics: access_key_env and secret_key_env must be set together")
		}
		accessKey, secretKey := os.Getenv(cfg.AccessKeyEnv), os.Getenv(cfg.SecretKeyEnv)
		if accessKey == "" || secretKey == "" {
			return "", "", "", fmt.Errorf("analytics: credential environment variables %s and %s must be set", cfg.AccessKeyEnv, cfg.SecretKeyEnv)
		}
		sessionToken := ""
		if cfg.SessionTokenEnv != "" {
			sessionToken = os.Getenv(cfg.SessionTokenEnv)
		}
		return accessKey, secretKey, sessionToken, nil
	}
	return "", "", "", nil
}

func newPipeline(cfg appconfig.AnalyticsConfig, put objectPutter) *Pipeline {
	cfg = cfg.Defaults()
	p := &Pipeline{cfg: cfg, put: put, queue: make(chan Event, cfg.QueueSize), stop: make(chan struct{}), done: make(chan struct{})}
	go p.run()
	return p
}

func (p *Pipeline) Record(ev Event) {
	if p == nil || p.put == nil {
		return
	}
	ev.SchemaVersion = 1
	if ev.Time == "" {
		ev.Time = time.Now().UTC().Format(time.RFC3339Nano)
	}
	select {
	case p.queue <- ev:
	default:
		p.dropped.Add(1)
	}
}

func (p *Pipeline) Status() Status {
	if p == nil || p.put == nil {
		return Status{}
	}
	return Status{Enabled: true, Bucket: p.cfg.Bucket, Prefix: p.cfg.Prefix, Queued: len(p.queue), Uploaded: p.uploaded.Load(), Dropped: p.dropped.Load(), SpooledBatches: p.spooled.Load(), UploadErrors: p.errors.Load()}
}

func (p *Pipeline) Close() {
	if p == nil || p.put == nil {
		return
	}
	p.once.Do(func() { close(p.stop); <-p.done })
}

func (p *Pipeline) run() {
	defer close(p.done)
	ticker := time.NewTicker(p.cfg.FlushInterval)
	defer ticker.Stop()
	batch := make([]Event, 0, p.cfg.BatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		copyBatch := append([]Event(nil), batch...)
		batch = batch[:0]
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := p.upload(ctx, copyBatch)
		cancel()
		if err != nil {
			p.errors.Add(1)
			if p.spool(copyBatch) == nil {
				p.spooled.Add(1)
			} else {
				p.dropped.Add(int64(len(copyBatch)))
			}
		}
	}
	for {
		select {
		case ev := <-p.queue:
			batch = append(batch, ev)
			if len(batch) >= p.cfg.BatchSize {
				flush()
			}
		case <-ticker.C:
			p.retrySpool()
			flush()
		case <-p.stop:
			for {
				select {
				case ev := <-p.queue:
					batch = append(batch, ev)
				default:
					flush()
					return
				}
			}
		}
	}
}

func (p *Pipeline) upload(ctx context.Context, events []Event) error {
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	for _, ev := range events {
		if err := enc.Encode(ev); err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	key := fmt.Sprintf("%s/year=%04d/month=%02d/day=%02d/hour=%02d/%d-%s.jsonl", strings.Trim(p.cfg.Prefix, "/"), now.Year(), now.Month(), now.Day(), now.Hour(), now.UnixMilli(), uuid.NewString())
	_, err := p.put.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(p.cfg.Bucket), Key: aws.String(key), Body: bytes.NewReader(body.Bytes()), ContentType: aws.String("application/x-ndjson")})
	if err == nil {
		p.uploaded.Add(int64(len(events)))
	}
	return err
}

func (p *Pipeline) spool(events []Event) error {
	if err := os.MkdirAll(p.cfg.SpoolDir, 0o700); err != nil {
		return err
	}
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	for _, ev := range events {
		if err := enc.Encode(ev); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(p.cfg.SpoolDir)
	if err != nil {
		return err
	}
	var used int64
	for _, entry := range entries {
		if info, e := entry.Info(); e == nil {
			used += info.Size()
		}
	}
	if used+int64(body.Len()) > p.cfg.SpoolMaxBytes {
		return fmt.Errorf("analytics spool limit reached")
	}
	path := filepath.Join(p.cfg.SpoolDir, fmt.Sprintf("%d-%s.jsonl", time.Now().UTC().UnixMilli(), uuid.NewString()))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(body.Bytes())
	return err
}

func (p *Pipeline) retrySpool() {
	entries, err := os.ReadDir(p.cfg.SpoolDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(p.cfg.SpoolDir, entry.Name())
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, err = p.put.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(p.cfg.Bucket), Key: aws.String(strings.Trim(p.cfg.Prefix, "/") + "/recovered/" + entry.Name()), Body: bytes.NewReader(body), ContentType: aws.String("application/x-ndjson")})
		cancel()
		if err != nil {
			p.errors.Add(1)
			return
		}
		p.uploaded.Add(int64(bytes.Count(body, []byte{'\n'})))
		_ = os.Remove(path)
	}
}
