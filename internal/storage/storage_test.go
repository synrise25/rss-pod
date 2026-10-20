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

func TestNewHonorsForcePathStyle(t *testing.T) {
	for _, tc := range []struct {
		name           string
		forcePathStyle bool
		wantHost       string
		wantPath       string
	}{
		{name: "path style", forcePathStyle: true, wantHost: "storage.example.com", wantPath: "/private/segments/audio.mp3"},
		{name: "bucket subdomain", forcePathStyle: false, wantHost: "private.storage.example.com", wantPath: "/segments/audio.mp3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := New(config.StorageConfig{
				Endpoint:       "https://storage.example.com",
				Region:         "us-east-1",
				AccessKey:      "test-access-key",
				SecretKey:      "test-secret-key",
				PrivateBucket:  "private",
				ForcePathStyle: tc.forcePathStyle,
			})
			if err != nil {
				t.Fatal(err)
			}
			// A fixed region and static credentials keep URL generation offline.
			requestURL, err := client.client.PresignedPutObject(context.Background(), client.config.PrivateBucket, "segments/audio.mp3", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if requestURL.Scheme != "https" || requestURL.Host != tc.wantHost || requestURL.Path != tc.wantPath {
				t.Fatalf("request URL = %s://%s%s, want https://%s%s", requestURL.Scheme, requestURL.Host, requestURL.Path, tc.wantHost, tc.wantPath)
			}
		})
	}
}

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

func TestDeletePrivateHonorsOperationTimeout(t *testing.T) {
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
	err = client.DeletePrivate(context.Background(), "segment.mp3")
	if err == nil {
		t.Fatal("DeletePrivate unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Fatalf("DeletePrivate error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("DeletePrivate took %s, want less than 1s", elapsed)
	}
}
