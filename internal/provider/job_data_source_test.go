package provider

import (
	"encoding/json"
	"testing"

	fwdatasource "github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestJobDataSourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwdatasource.SchemaResponse{}

	NewJobDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Schema method diagnostics: %+v", resp.Diagnostics)
	}
	if diagnostics := resp.Schema.ValidateImplementation(ctx); diagnostics.HasError() {
		t.Fatalf("Schema validation diagnostics: %+v", diagnostics)
	}
}

func TestLatestJob(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    string
		wantID  int64
		wantNil bool
		wantErr bool
	}{
		{
			name:    "empty list means no job yet",
			body:    `{"count":0,"results":[]}`,
			wantNil: true,
		},
		{
			name:   "first result is the most recent",
			body:   `{"count":2,"results":[{"id":7038,"status":"failed"},{"id":7036,"status":"failed"}]}`,
			wantID: 7038,
		},
		{
			name:    "a non-list body is an error",
			body:    `Not valid JSON`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, diags := latestJob([]byte(tt.body))

			if diags.HasError() != tt.wantErr {
				t.Fatalf("latestJob(%s) error = %v, want %v", tt.body, diags.HasError(), tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if tt.wantNil {
				if got != nil {
					t.Fatalf("latestJob(%s) = %s, want nil", tt.body, got)
				}
				return
			}

			var job JobDataSourceAPIModel
			if err := json.Unmarshal(got, &job); err != nil {
				t.Fatalf("unmarshalling %s: %s", got, err)
			}
			if job.ID != tt.wantID {
				t.Fatalf("latestJob(%s) id = %d, want %d", tt.body, job.ID, tt.wantID)
			}
		})
	}
}

// A job that has not left pending has null started, finished and elapsed in AAP.
// Those must come through as null rather than fail the parse.
func TestJobDataSourcePendingJob(t *testing.T) {
	t.Parallel()

	var job JobDataSourceAPIModel
	body := `{"id":1,"url":"/api/controller/v2/jobs/1/","status":"pending","failed":false,
		"started":null,"finished":null,"elapsed":null,"limit":"lx1-guest","job_explanation":""}`

	if err := json.Unmarshal([]byte(body), &job); err != nil {
		t.Fatalf("unmarshalling a pending job: %s", err)
	}

	if !optionalString(job.Started).IsNull() {
		t.Errorf("started = %v, want null", optionalString(job.Started))
	}
	if !optionalFloat(job.Elapsed).IsNull() {
		t.Errorf("elapsed = %v, want null", optionalFloat(job.Elapsed))
	}
	if optionalString(job.Finished).IsNull() != true {
		t.Errorf("finished = %v, want null", optionalString(job.Finished))
	}

	started := "2026-09-25T21:09:00Z"
	elapsed := 908.493
	if got := optionalString(&started); got.ValueString() != started {
		t.Errorf("started = %v, want %q", got, started)
	}
	if got := optionalFloat(&elapsed); got.ValueFloat64() != elapsed {
		t.Errorf("elapsed = %v, want %v", got, elapsed)
	}
	if got := optionalString(nil); !got.IsNull() {
		t.Errorf("optionalString(nil) = %v, want null", got)
	}
	if got := optionalFloat(nil); got != types.Float64Null() {
		t.Errorf("optionalFloat(nil) = %v, want null", got)
	}
}
