package storage

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/synrise25/rss-pod/internal/config"
)

type blockingTransport struct{}

func (blockingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	<-request.Context().Done()
	return nil, request.Context().Err()
}

func TestPutPrivateHonorsOperationTimeout(t *testing.T) {
	minioClient, err := minio.New("storage.test", &minio.Options{
		Creds:     credentials.NewStaticV4("access", "secret", ""),
		Secure:    true,
		Region:    "us-east-1",
		Transport: blockingTransport{},
	})
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{
		client:  minioClient,
		config:  config.StorageConfig{PrivateBucket: "private"},
		timeout: 50 * time.Millisecond,
	}

	started := time.Now()
	err = client.PutPrivate(context.Background(), "segment.mp3", "audio/mpeg", []byte("audio"))
	if err == nil {
		t.Fatal("PutPrivate unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Fatalf("PutPrivate error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("PutPrivate took %s, want less than 1s", elapsed)
	}
}
