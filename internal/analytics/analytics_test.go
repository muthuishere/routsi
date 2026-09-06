package analytics

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/muthuishere/routsi/internal/config"
)

type fakePutter struct {
	mu    sync.Mutex
	input *s3.PutObjectInput
	body  string
	err   error
}

func (f *fakePutter) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.input = in
	b, _ := io.ReadAll(in.Body)
	f.body = string(b)
	return &s3.PutObjectOutput{}, f.err
}

func TestPipelineUploadsPrivacySafeJSONL(t *testing.T) {
	put := &fakePutter{}
	p := newPipeline(config.AnalyticsConfig{Bucket: "usage", Prefix: "events", BatchSize: 1, QueueSize: 2, FlushInterval: time.Hour, SpoolDir: t.TempDir()}, put)
	p.Record(Event{SelectedModel: "claude", Provider: "bedrock", TotalTokens: 42})
	deadline := time.Now().Add(time.Second)
	for p.Status().Uploaded != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	p.Close()
	put.mu.Lock()
	defer put.mu.Unlock()
	if put.input == nil || put.input.Bucket == nil || *put.input.Bucket != "usage" {
		t.Fatal("missing S3 upload")
	}
	if !strings.Contains(put.body, `"selected_model":"claude"`) || !strings.Contains(put.body, `"schema_version":1`) {
		t.Fatalf("unexpected JSONL: %s", put.body)
	}
	for _, forbidden := range []string{"prompt", "response", "credential"} {
		if strings.Contains(put.body, `"`+forbidden+`"`) {
			t.Fatalf("stored forbidden field %q", forbidden)
		}
	}
}

func TestPipelineSpoolsWhenS3Fails(t *testing.T) {
	dir := t.TempDir()
	put := &fakePutter{err: errors.New("offline")}
	p := newPipeline(config.AnalyticsConfig{Bucket: "usage", BatchSize: 1, QueueSize: 2, FlushInterval: time.Hour, SpoolDir: dir}, put)
	p.Record(Event{SelectedModel: "devin"})
	deadline := time.Now().Add(time.Second)
	for p.Status().SpooledBatches != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	p.Close()
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("spool entries=%d err=%v", len(entries), err)
	}
}

func TestCustomCredentialEnvironmentNamesMustBePaired(t *testing.T) {
	_, err := New(context.Background(), config.AnalyticsConfig{Bucket: "usage", Region: "us-east-1", AccessKeyEnv: "ROUTSI_TEST_ACCESS_ONLY"})
	if err == nil || !strings.Contains(err.Error(), "must be set together") {
		t.Fatalf("expected paired credential error, got %v", err)
	}
}

func TestCustomCredentialEnvironmentNamesResolveWithoutExposingValues(t *testing.T) {
	t.Setenv("ROUTSI_TEST_S3_ACCESS", "fake-access")
	t.Setenv("ROUTSI_TEST_S3_SECRET", "fake-secret")
	p, err := New(context.Background(), config.AnalyticsConfig{Bucket: "usage", Region: "us-east-1", AccessKeyEnv: "ROUTSI_TEST_S3_ACCESS", SecretKeyEnv: "ROUTSI_TEST_S3_SECRET"})
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
}
