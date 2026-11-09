package roadmap

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestThemeGone(t *testing.T) {
	var ve *ValidationError
	fk := fmt.Errorf("insert: %w", &pgconn.PgError{Code: "23503"})
	if err := themeGone(fk); !errors.As(err, &ve) || ve.Msg != "Choose a theme." {
		t.Fatalf("FK violation = %v, want 400 Choose a theme.", err)
	}
	other := &pgconn.PgError{Code: "23505"}
	if err := themeGone(other); err != other {
		t.Fatalf("other error = %v, want unchanged", err)
	}
}
