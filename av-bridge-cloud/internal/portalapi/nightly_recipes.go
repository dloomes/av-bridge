package portalapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/audit"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/jackc/pgx/v5"
)

// Nightly Room Readiness — test-recipe CRUD.
//
// Slice 2B. Recipes are the reusable JSON step definitions the runner
// executes after power-on (see docs/nightly-lifecycle-spec.md §7). This
// slice only handles storage — actual step execution is Phase B.
//
// Endpoints:
//   GET    /api/v1/nightly/recipes         list customer's recipes
//   GET    /api/v1/nightly/recipes/{id}    single recipe with steps
//   POST   /api/v1/nightly/recipes         create
//   PATCH  /api/v1/nightly/recipes/{id}    update
//   DELETE /api/v1/nightly/recipes/{id}    delete (recipe references in
//                                          nightly_schedule / room_nightly_config
//                                          are ON DELETE SET NULL, so
//                                          schedules just lose their test)

// recipeListRow — trimmed shape for the list endpoint. Steps are omitted
// because most recipes are 5-15 steps each; the list is a picker, not a
// browse-all view. Detail endpoint returns the full steps array.
type recipeListRow struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	StepCount   int    `json:"step_count"`
	UpdatedAt   string `json:"updated_at"`
}

type recipeDetail struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Steps       json.RawMessage `json:"steps"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
}

// ListNightlyRecipes — GET /api/v1/nightly/recipes
func (h *Handler) ListNightlyRecipes(w http.ResponseWriter, r *http.Request) {
	out := []recipeListRow{}
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, name, description, jsonb_array_length(steps), updated_at
			  FROM nightly_test_recipe
			 ORDER BY lower(name)
		`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				row  recipeListRow
				desc *string
				upd  time.Time
			)
			if err := rows.Scan(&row.ID, &row.Name, &desc, &row.StepCount, &upd); err != nil {
				return err
			}
			if desc != nil {
				row.Description = *desc
			}
			row.UpdatedAt = upd.UTC().Format(time.RFC3339)
			out = append(out, row)
		}
		return rows.Err()
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// GetNightlyRecipe — GET /api/v1/nightly/recipes/{id}
func (h *Handler) GetNightlyRecipe(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "recipe id required")
		return
	}
	var (
		out      recipeDetail
		notFound bool
	)
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		var (
			desc   *string
			steps  []byte
			cre    time.Time
			upd    time.Time
		)
		err := tx.QueryRow(ctx, `
			SELECT id::text, name, description, steps, created_at, updated_at
			  FROM nightly_test_recipe
			 WHERE id = $1
		`, id).Scan(&out.ID, &out.Name, &desc, &steps, &cre, &upd)
		if errors.Is(err, pgx.ErrNoRows) {
			notFound = true
			return nil
		}
		if err != nil {
			return err
		}
		if desc != nil {
			out.Description = *desc
		}
		out.Steps = json.RawMessage(steps)
		out.CreatedAt = cre.UTC().Format(time.RFC3339)
		out.UpdatedAt = upd.UTC().Format(time.RFC3339)
		return nil
	})
	if !ok {
		return
	}
	if notFound {
		writeErr(w, http.StatusNotFound, "recipe not found")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// createRecipeReq is the wire shape for POST. Steps must be a JSON array
// but we don't validate step-object shape here — Phase B (the runner) is
// the source of truth for what a valid step is, and validating shape in
// two places invites drift.
type createRecipeReq struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Steps       json.RawMessage `json:"steps"`
}

// CreateNightlyRecipe — POST /api/v1/nightly/recipes
func (h *Handler) CreateNightlyRecipe(w http.ResponseWriter, r *http.Request) {
	var req createRecipeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	steps, err := normaliseStepsJSON(req.Steps)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "steps: "+err.Error())
		return
	}

	p, _ := portalauth.From(r.Context())
	var newID string
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		var descArg any
		if strings.TrimSpace(req.Description) != "" {
			descArg = strings.TrimSpace(req.Description)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO nightly_test_recipe (customer_id, name, description, steps)
			VALUES ($1, $2, $3, $4::jsonb)
			RETURNING id::text
		`, p.CustomerID, name, descArg, steps).Scan(&newID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action:     "nightly.recipe.create",
			TargetKind: "nightly_recipe", TargetID: newID,
			After: mustJSON(map[string]any{
				"name":       name,
				"step_count": approximateStepCount(steps),
			}),
		}))
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": newID})
}

// updateRecipeReq — pointer fields so absent = leave alone. Steps + name
// are always fully replaced (no merge semantics inside the array).
type updateRecipeReq struct {
	Name        *string          `json:"name,omitempty"`
	Description *string          `json:"description,omitempty"`
	Steps       *json.RawMessage `json:"steps,omitempty"`
}

// UpdateNightlyRecipe — PATCH /api/v1/nightly/recipes/{id}
func (h *Handler) UpdateNightlyRecipe(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "recipe id required")
		return
	}
	var req updateRecipeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Name != nil && strings.TrimSpace(*req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name cannot be empty")
		return
	}
	var normalisedSteps []byte
	if req.Steps != nil {
		s, err := normaliseStepsJSON(*req.Steps)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "steps: "+err.Error())
			return
		}
		normalisedSteps = s
	}

	p, _ := portalauth.From(r.Context())
	notFound := false
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		// Verify the recipe exists inside the caller's scope first.
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT true FROM nightly_test_recipe WHERE id = $1`, id,
		).Scan(&exists); errors.Is(err, pgx.ErrNoRows) {
			notFound = true
			return nil
		} else if err != nil {
			return err
		}

		set := []string{}
		args := []any{id}
		add := func(col string, val any) {
			args = append(args, val)
			set = append(set, fmt.Sprintf("%s = $%d", col, len(args)))
		}
		if req.Name != nil {
			add("name", strings.TrimSpace(*req.Name))
		}
		if req.Description != nil {
			d := strings.TrimSpace(*req.Description)
			if d == "" {
				add("description", nil)
			} else {
				add("description", d)
			}
		}
		if normalisedSteps != nil {
			// Explicit jsonb cast so pgx treats the bytes as JSON not text.
			set = append(set, fmt.Sprintf("steps = $%d::jsonb", len(args)+1))
			args = append(args, normalisedSteps)
		}
		if len(set) == 0 {
			return nil
		}
		sql := "UPDATE nightly_test_recipe SET " + strings.Join(set, ", ") +
			" WHERE id = $1"
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return err
		}

		payload := map[string]any{"id": id}
		if req.Name != nil {
			payload["name"] = strings.TrimSpace(*req.Name)
		}
		if req.Description != nil {
			payload["description"] = strings.TrimSpace(*req.Description)
		}
		if normalisedSteps != nil {
			payload["step_count"] = approximateStepCount(normalisedSteps)
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action:     "nightly.recipe.update",
			TargetKind: "nightly_recipe", TargetID: id,
			After: mustJSON(payload),
		}))
	})
	if !ok {
		return
	}
	if notFound {
		writeErr(w, http.StatusNotFound, "recipe not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteNightlyRecipe — DELETE /api/v1/nightly/recipes/{id}
//
// The schedule + room_override FK columns are ON DELETE SET NULL, so
// dropping a recipe doesn't cascade — dependent rows just lose their
// test_recipe_id and fall back to "power cycle only, no test". Callers
// see 204 on success, 404 if the recipe wasn't visible.
func (h *Handler) DeleteNightlyRecipe(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "recipe id required")
		return
	}
	p, _ := portalauth.From(r.Context())
	notFound := false
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		res, err := tx.Exec(ctx,
			`DELETE FROM nightly_test_recipe WHERE id = $1`, id)
		if err != nil {
			return err
		}
		if res.RowsAffected() == 0 {
			notFound = true
			return nil
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action:     "nightly.recipe.delete",
			TargetKind: "nightly_recipe", TargetID: id,
			After: mustJSON(map[string]any{"id": id}),
		}))
	})
	if !ok {
		return
	}
	if notFound {
		writeErr(w, http.StatusNotFound, "recipe not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// normaliseStepsJSON accepts the raw steps payload and returns a cleaned
// []byte suitable for a jsonb cast. Empty / null / undefined become "[]"
// so a fresh recipe can be saved before any steps are authored. Anything
// else must parse as a JSON array; step-object shape is Phase B's problem.
func normaliseStepsJSON(raw json.RawMessage) ([]byte, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return []byte("[]"), nil
	}
	// Round-trip through Unmarshal to reject malformed JSON early with a
	// clear message. The []any target proves it's an array.
	var arr []any
	if err := json.Unmarshal([]byte(trimmed), &arr); err != nil {
		return nil, errors.New("must be a JSON array")
	}
	// Re-marshal so we normalise whitespace and reject any smuggled
	// trailing content past the array.
	clean, err := json.Marshal(arr)
	if err != nil {
		return nil, errors.New("could not re-encode steps: " + err.Error())
	}
	return clean, nil
}

// approximateStepCount is used for audit metadata + list summary. Cheap
// because the caller already has the marshalled bytes.
func approximateStepCount(steps []byte) int {
	var arr []any
	_ = json.Unmarshal(steps, &arr)
	return len(arr)
}
