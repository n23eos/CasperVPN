package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/caspervpn/billing/internal/model"
	"github.com/caspervpn/billing/internal/store"
)

func TestPostgres_ReadModelsMatchMemoryOrderingAndFiltering(t *testing.T) {
	pool := newTestPool(t)
	defer pool.Close()
	ctx := context.Background()
	pg := store.NewPostgres(pool)
	memory := store.NewMemory()
	created := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	invoices := []model.Invoice{
		{ID: "read-a", AnonUserID: "acct-1", Plan: "basic", Status: model.StatusPending, Amount: "1", Currency: "BTC", CreatedAt: created, ExpiresAt: created.Add(time.Hour)},
		{ID: "read-z", AnonUserID: "acct-1", Plan: "unlimited", Status: model.StatusSettled, Amount: "2", Currency: "BTC", CreatedAt: created, ExpiresAt: created.Add(time.Hour)},
		{ID: "read-other", AnonUserID: "acct-2", Plan: "basic", Status: model.StatusInvalid, Amount: "3", Currency: "BTC", CreatedAt: created.Add(time.Hour), ExpiresAt: created.Add(2 * time.Hour)},
		{ID: "read-old", AnonUserID: "acct-3", Plan: "basic", Status: model.StatusExpired, Amount: "4", Currency: "BTC", CreatedAt: created.Add(-time.Hour), ExpiresAt: created},
	}
	for i := range invoices {
		invoices[i].Provider = "mock"
		invoices[i].ProviderInvoiceID = "provider-" + invoices[i].ID
		invoices[i].PayAddress = "address-" + invoices[i].ID
		if err := pg.CreateInvoice(ctx, invoices[i]); err != nil {
			t.Fatalf("create pg invoice: %v", err)
		}
		if err := memory.CreateInvoice(ctx, invoices[i]); err != nil {
			t.Fatalf("create memory invoice: %v", err)
		}
	}

	pgLatest, err := pg.LatestInvoice(ctx, "acct-1")
	if err != nil {
		t.Fatalf("pg latest: %v", err)
	}
	memLatest, err := memory.LatestInvoice(ctx, "acct-1")
	if err != nil {
		t.Fatalf("memory latest: %v", err)
	}
	if pgLatest != memLatest || pgLatest.InvoiceID != "read-z" {
		t.Fatalf("latest pg=%+v memory=%+v", pgLatest, memLatest)
	}
	if _, err := pg.LatestInvoice(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing latest err = %v, want ErrNotFound", err)
	}

	pgCounts, pgRecent, err := pg.InvoiceSummary(ctx, 50)
	if err != nil {
		t.Fatalf("pg summary: %v", err)
	}
	memCounts, memRecent, err := memory.InvoiceSummary(ctx, 50)
	if err != nil {
		t.Fatalf("memory summary: %v", err)
	}
	if pgCounts != memCounts || !reflect.DeepEqual(pgRecent, memRecent) {
		t.Fatalf("summary mismatch pg=%+v/%+v memory=%+v/%+v", pgCounts, pgRecent, memCounts, memRecent)
	}
}
