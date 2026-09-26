package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	tfpath "github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// JobDataSourceAPIModel is the part of the AAP job payload this data source reads.
// started, finished and elapsed are null in AAP until a job leaves pending, so
// they are pointers rather than zero values.
type JobDataSourceAPIModel struct {
	ID             int64    `json:"id"`
	URL            string   `json:"url"`
	Status         string   `json:"status"`
	Failed         bool     `json:"failed"`
	Started        *string  `json:"started"`
	Finished       *string  `json:"finished"`
	Elapsed        *float64 `json:"elapsed"`
	Limit          string   `json:"limit"`
	JobExplanation string   `json:"job_explanation"`
}

type jobListAPIModel struct {
	Count   int               `json:"count"`
	Results []json.RawMessage `json:"results"`
}

// JobDataSourceModel maps the data source schema data.
type JobDataSourceModel struct {
	ID             types.Int64   `tfsdk:"id"`
	JobTemplateID  types.Int64   `tfsdk:"job_template_id"`
	Limit          types.String  `tfsdk:"limit"`
	URL            types.String  `tfsdk:"url"`
	Status         types.String  `tfsdk:"status"`
	Failed         types.Bool    `tfsdk:"failed"`
	Started        types.String  `tfsdk:"started"`
	Finished       types.String  `tfsdk:"finished"`
	Elapsed        types.Float64 `tfsdk:"elapsed"`
	JobExplanation types.String  `tfsdk:"job_explanation"`
	Stdout         types.String  `tfsdk:"stdout"`
}

// JobDataSource is the data source implementation.
type JobDataSource struct {
	client ProviderHTTPClient
}

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource                     = &JobDataSource{}
	_ datasource.DataSourceWithConfigure        = &JobDataSource{}
	_ datasource.DataSourceWithConfigValidators = &JobDataSource{}
)

// NewJobDataSource is a helper function to simplify the provider implementation.
func NewJobDataSource() datasource.DataSource {
	return &JobDataSource{}
}

func (d *JobDataSource) Metadata(_ context.Context, req datasource.MetadataRequest,
	resp *datasource.MetadataResponse) {
	resp.TypeName = fmt.Sprintf("%s_job", req.ProviderTypeName)
}

func (d *JobDataSource) Configure(_ context.Context, req datasource.ConfigureRequest,
	resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*AAPClient)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *AAPClient, got: %T. Please report this issue to the provider developers.",
				req.ProviderData),
		)
		return
	}
	d.client = client
}

func (d *JobDataSource) Schema(_ context.Context, _ datasource.SchemaRequest,
	resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Get an existing AAP job, either by id or as the most recent job for a job template. " +
			"Useful for reading back the result of a job launched by the aap_job_launch action, which " +
			"cannot return values itself.",
		Attributes: map[string]schema.Attribute{
			"id": schema.Int64Attribute{
				Optional:    true,
				Computed:    true,
				Description: "ID of the job. If not set, the most recent job matching the other attributes is read.",
			},
			"job_template_id": schema.Int64Attribute{
				Optional:    true,
				Description: "ID of the job template to read the most recent job of.",
			},
			"limit": schema.StringAttribute{
				Optional:    true,
				Description: "Only consider jobs launched with this limit pattern, for example a host name.",
			},
			"url": schema.StringAttribute{
				Computed:    true,
				Description: "URL of the job.",
			},
			"status": schema.StringAttribute{
				Computed:    true,
				Description: "Status of the job: new, pending, waiting, running, successful, failed or canceled.",
			},
			"failed": schema.BoolAttribute{
				Computed:    true,
				Description: "Whether the job failed.",
			},
			"started": schema.StringAttribute{
				Computed:    true,
				Description: "When the job started.",
			},
			"finished": schema.StringAttribute{
				Computed:    true,
				Description: "When the job finished.",
			},
			"elapsed": schema.Float64Attribute{
				Computed:    true,
				Description: "How long the job ran, in seconds.",
			},
			"job_explanation": schema.StringAttribute{
				Computed:    true,
				Description: "Why the job is in its current state, as reported by AAP.",
			},
			"stdout": schema.StringAttribute{
				Computed:    true,
				Description: "Job output as text.",
			},
		},
	}
}

func (d *JobDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.AtLeastOneOf(
			tfpath.MatchRoot("id"),
			tfpath.MatchRoot("job_template_id"),
		),
	}
}

// Read reads the job from AAP. Nothing found is a warning rather than an error:
// a plan that runs before the first job has been launched must still succeed.
func (d *JobDataSource) Read(ctx context.Context, req datasource.ReadRequest,
	resp *datasource.ReadResponse) {
	var data JobDataSourceModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body, diags := d.findJob(data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() || body == nil {
		return
	}

	var job JobDataSourceAPIModel
	if err := json.Unmarshal(body, &job); err != nil {
		resp.Diagnostics.AddError("Error parsing JSON response from AAP",
			fmt.Sprintf("Could not parse job: %s", err.Error()))
		return
	}

	data.ID = types.Int64Value(job.ID)
	data.URL = types.StringValue(job.URL)
	data.Status = types.StringValue(job.Status)
	data.Failed = types.BoolValue(job.Failed)
	data.Started = optionalString(job.Started)
	data.Finished = optionalString(job.Finished)
	data.Elapsed = optionalFloat(job.Elapsed)
	data.JobExplanation = types.StringValue(job.JobExplanation)
	data.Stdout = d.readStdout(job.ID, &resp.Diagnostics)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// findJob returns the raw job payload, the most recent one for a job template
// when no id was given.
func (d *JobDataSource) findJob(data JobDataSourceModel) ([]byte, diag.Diagnostics) {
	var diags diag.Diagnostics

	if IsValueProvidedOrPromised(data.ID) {
		body, d2, status := d.client.GetWithStatus(
			path.Join(d.client.getAPIEndpoint(), "jobs", strconv.FormatInt(data.ID.ValueInt64(), 10)), nil)
		diags.Append(d2...)
		if diags.HasError() {
			return nil, diags
		}
		if status == http.StatusNotFound {
			diags.AddWarning("Job not found",
				fmt.Sprintf("No job with id %d on the AAP host.", data.ID.ValueInt64()))
			return nil, diags
		}
		return body, diags
	}

	params := map[string]string{
		"job_template__id": strconv.FormatInt(data.JobTemplateID.ValueInt64(), 10),
		"order_by":         "-id",
		"page_size":        "1",
	}
	if IsValueProvidedOrPromised(data.Limit) {
		params["limit"] = data.Limit.ValueString()
	}

	body, d2, status := d.client.GetWithStatus(path.Join(d.client.getAPIEndpoint(), "jobs"), params)
	diags.Append(d2...)
	if diags.HasError() {
		return nil, diags
	}
	if status == http.StatusForbidden {
		diags.AddWarning("Not allowed to read jobs",
			"The AAP user the provider is configured with cannot read jobs. Give it read access to the "+
				"job template (or the organization) to read job results back.")
		return nil, diags
	}

	latest, d3 := latestJob(body)
	diags.Append(d3...)
	if diags.HasError() {
		return nil, diags
	}
	if latest == nil {
		diags.AddWarning("No job found",
			fmt.Sprintf("Job template %d has no job with limit %q yet.",
				data.JobTemplateID.ValueInt64(), data.Limit.ValueString()))
		return nil, diags
	}
	return latest, diags
}

// readStdout fetches the job's output. AAP serves it separately from the job
// payload, and returns 404 while a job has not produced output yet.
func (d *JobDataSource) readStdout(jobID int64, diags *diag.Diagnostics) types.String {
	if jobID == 0 {
		return types.StringNull()
	}

	body, d2, status := d.client.GetWithStatus(
		path.Join(d.client.getAPIEndpoint(), "jobs", strconv.FormatInt(jobID, 10), "stdout"),
		map[string]string{"format": "json"})
	diags.Append(d2...)
	if diags.HasError() {
		return types.StringNull()
	}
	if status == http.StatusNotFound {
		return types.StringNull()
	}

	var out struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		diags.AddWarning("Could not parse job output",
			fmt.Sprintf("Could not parse the stdout of job %d: %s", jobID, err.Error()))
		return types.StringNull()
	}
	return types.StringValue(out.Content)
}

// latestJob picks the single job out of a paginated AAP list response.
func latestJob(body []byte) (json.RawMessage, diag.Diagnostics) {
	var diags diag.Diagnostics

	var list jobListAPIModel
	if err := json.Unmarshal(body, &list); err != nil {
		diags.AddError("Error parsing JSON response from AAP",
			fmt.Sprintf("Could not parse job list: %s", err.Error()))
		return nil, diags
	}
	if len(list.Results) == 0 {
		return nil, diags
	}
	return list.Results[0], diags
}

func optionalString(v *string) types.String {
	if v == nil {
		return types.StringNull()
	}
	return types.StringValue(*v)
}

func optionalFloat(v *float64) types.Float64 {
	if v == nil {
		return types.Float64Null()
	}
	return types.Float64Value(*v)
}
