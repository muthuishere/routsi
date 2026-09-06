package analytics

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/muthuishere/routsi/internal/config"
)

func TestLiveMinIOUpload(t *testing.T) {
	if testing.Short() || os.Getenv("ROUTSI_MINIO_E2E") == "" {
		t.Skip("set ROUTSI_MINIO_E2E=1 to test the included MinIO stack")
	}
	cfg := config.AnalyticsConfig{Bucket: "routsi-analytics", Prefix: "e2e", Region: "us-east-1", Endpoint: "http://127.0.0.1:19000", UsePathStyle: true, BatchSize: 1, FlushInterval: time.Second, SpoolDir: t.TempDir()}
	p, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	p.Record(Event{SelectedModel: "e2e-model", Provider: "test", TotalTokens: 7})
	deadline := time.Now().Add(10 * time.Second)
	for p.Status().Uploaded != 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	p.Close()
	if p.Status().Uploaded != 1 {
		t.Fatalf("upload status: %#v", p.Status())
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion("us-east-1"))
	if err != nil {
		t.Fatal(err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) { o.BaseEndpoint = aws.String("http://127.0.0.1:19000"); o.UsePathStyle = true })
	out, err := client.ListObjectsV2(context.Background(), &s3.ListObjectsV2Input{Bucket: aws.String("routsi-analytics"), Prefix: aws.String("e2e/")})
	if err != nil || len(out.Contents) == 0 {
		t.Fatalf("uploaded object not found: objects=%d err=%v", len(out.Contents), err)
	}
}
