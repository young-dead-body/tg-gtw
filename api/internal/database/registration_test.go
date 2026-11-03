package database

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/company/telegram-gateway/api/internal/db"
)

func TestRegistrationRequestQueries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dsn := testDSN(t)

	if err := MigrateDown(ctx, dsn, ""); err != nil {
		t.Logf("clean down before test: %v", err)
	}
	if err := Migrate(ctx, dsn, ""); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	pool, err := Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	q := db.New(pool)

	created, err := q.CreateRegistrationRequest(ctx, db.CreateRegistrationRequestParams{
		FirstName:  "Пётр",
		LastName:   "Петров",
		MiddleName: pgtype.Text{String: "Петрович", Valid: true},
		Email:      "petr@example.com",
		Telegram:   pgtype.Text{String: "@petrov", Valid: true},
		Department: pgtype.Text{String: "IT", Valid: true},
		Position:   pgtype.Text{String: "Разработчик", Valid: true},
		Reason:     pgtype.Text{String: "Доступ к каналу", Valid: true},
	})
	if err != nil {
		t.Fatalf("create registration: %v", err)
	}
	if created.Status != "pending" {
		t.Errorf("expected status pending, got %q", created.Status)
	}

	fetched, err := q.GetRegistrationRequest(ctx, created.ID)
	if err != nil {
		t.Fatalf("get registration: %v", err)
	}
	if fetched.Email != "petr@example.com" {
		t.Errorf("unexpected email %q", fetched.Email)
	}
	if !fetched.MiddleName.Valid || fetched.MiddleName.String != "Петрович" {
		t.Errorf("expected middle name, got %v", fetched.MiddleName)
	}

	list, err := q.ListRegistrationRequests(ctx)
	if err != nil {
		t.Fatalf("list registration: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 registration, got %d", len(list))
	}

	approved := "approved"
	updated, err := q.UpdateRegistrationRequestStatus(ctx, db.UpdateRegistrationRequestStatusParams{
		ID:     created.ID,
		Status: approved,
	})
	if err != nil {
		t.Fatalf("update status: %v", err)
	}
	if updated.Status != "approved" {
		t.Errorf("expected status approved, got %q", updated.Status)
	}

	if err := MigrateDown(ctx, dsn, ""); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
}
