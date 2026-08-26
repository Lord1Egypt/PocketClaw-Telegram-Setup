package pairing

import (
	"context"
	"errors"
)

// ErrStorageNotConfigured is returned by every operation when no shared
// storage backend has been configured.
var ErrStorageNotConfigured = errors.New(
	"pairing storage is not configured: connect a Redis database to this project")

// UnconfiguredStore is the production stand-in used when no shared storage is
// configured. Every operation fails.
//
// This exists because the alternative is worse. Falling back to the in-memory
// store would let a deployment look healthy: creating a pairing would succeed,
// the app would show a QR code, and the flow would then never complete —
// because the Telegram webhook lands in a different function instance that has
// never heard of that pairing. That failure is invisible at the moment it is
// caused and baffling ten minutes later. Failing at the first request instead
// points straight at the missing database.
type UnconfiguredStore struct{}

// NewUnconfiguredStore returns a store that refuses every operation.
func NewUnconfiguredStore() *UnconfiguredStore { return &UnconfiguredStore{} }

func (*UnconfiguredStore) Describe() string { return "Not configured" }

func (*UnconfiguredStore) Ping(context.Context) error { return ErrStorageNotConfigured }

func (*UnconfiguredStore) Create(context.Context, Record) error { return ErrStorageNotConfigured }

func (*UnconfiguredStore) Get(context.Context, string) (Record, error) {
	return Record{}, ErrStorageNotConfigured
}

func (*UnconfiguredStore) FindByUsername(context.Context, string) (Record, error) {
	return Record{}, ErrStorageNotConfigured
}

func (*UnconfiguredStore) Update(context.Context, Record) error { return ErrStorageNotConfigured }

func (*UnconfiguredStore) PutToken(context.Context, string, string) error {
	return ErrStorageNotConfigured
}

func (*UnconfiguredStore) TakeToken(context.Context, string) (string, error) {
	return "", ErrStorageNotConfigured
}

func (*UnconfiguredStore) Delete(context.Context, string) error { return ErrStorageNotConfigured }
