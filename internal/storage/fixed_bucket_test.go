package storage_test

import (
	"errors"
	"github.com/razzie-cloud/notes/internal/storage"
	"github.com/razzie-cloud/notes/internal/store"
	"testing"
)

func TestFixedBucketCannotBeOverriddenAtStorageBoundary(t *testing.T) {
	registry := &storage.Registry{}
	_, _, err := registry.Direct(store.Connection{Bucket: "fixed-bucket"}, store.Notebook{Bucket: "other-bucket"}, store.Secrets{})
	if !errors.Is(err, store.ErrBucket) {
		t.Fatal("storage constructor allowed bucket override", err)
	}
}
