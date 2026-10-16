package mcptools

import (
	"context"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/mcptools/format"
)

// TaskAddIn is the task_add input. It carries a tool profile name, never a
// tool list; validation is api.TaskSpec.Validate (via store.Add).
type TaskAddIn struct {
	From           string  `json:"from,omitempty" jsonschema:"your session id (notified when done; default repo)"`
	Repo           string  `json:"repo,omitempty" jsonschema:"git remote or repo key"`
	Prompt         string  `json:"prompt"`
	BaseBranch     string  `json:"base_branch,omitempty"`
	BudgetUSD      float64 `json:"budget_usd"`
	PermissionMode string  `json:"permission_mode,omitempty" jsonschema:"acceptEdits|dontAsk|plan"`
	ToolProfile    string  `json:"tool_profile,omitempty" jsonschema:"host profile name"`
	Host           string  `json:"host,omitempty"`
	MaxAttempts    int     `json:"max_attempts,omitempty"`
	TargetHost     string  `json:"target_host,omitempty" jsonschema:"run on this host only"`
	TargetSession  string  `json:"target_session,omitempty" jsonschema:"session id, prefix or name"`
	RequestID      string  `json:"request_id,omitempty" jsonschema:"generate a unique id per logical task and reuse it if you retry this call"`
}

// TaskListIn filters task_list.
type TaskListIn struct {
	State      string `json:"state,omitempty" jsonschema:"queued|running|done|stuck|cancelled"`
	SinceHours int    `json:"since_hours,omitempty"`
}

// TaskIDIn names one task.
type TaskIDIn struct {
	ID int64 `json:"id"`
}

func (t tools) addTaskTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{Name: "task_add", Description: "Queue a task for a daemon or another session."}, t.taskAdd)
	mcp.AddTool(srv, &mcp.Tool{Name: "task_list", Description: "List tasks, newest first."}, t.taskList)
	mcp.AddTool(srv, &mcp.Tool{Name: "task_get", Description: "Show one task."}, t.taskGet)
	if t.p.Admin {
		mcp.AddTool(srv, &mcp.Tool{Name: "task_cancel", Description: "Cancel a task (admin)."}, t.taskCancel)
		mcp.AddTool(srv, &mcp.Tool{Name: "task_requeue", Description: "Requeue a stuck/cancelled task (admin)."}, t.taskRequeue)
	}
}

func (t tools) taskAdd(ctx context.Context, _ *mcp.CallToolRequest, in TaskAddIn) (*mcp.CallToolResult, any, error) {
	spec := api.TaskSpec{Repo: in.Repo, Prompt: in.Prompt, BaseBranch: in.BaseBranch, MaxBudgetUSD: in.BudgetUSD,
		PermissionMode: in.PermissionMode, ToolProfile: in.ToolProfile, Host: in.Host, MaxAttempts: in.MaxAttempts,
		TargetHost: in.TargetHost, CreatedBySession: in.From, RequestID: in.RequestID}
	if in.TargetSession != "" {
		target, res := t.resolveTarget(ctx, in.TargetSession)
		if res != nil {
			return res, nil, nil
		}
		spec.TargetSession = target.ID
	}
	if spec.Repo == "" && in.From != "" {
		if from, err := t.st.ListSessions(ctx, false); err == nil {
			for _, s := range from {
				if s.ID == in.From {
					spec.Repo = s.Repo
				}
			}
		}
	}
	if spec.Repo == "" {
		return errResult("repo is required (or pass from= a session registered with a git remote)"), nil, nil
	}
	task, replayed, err := t.st.AddTaskReplay(ctx, spec, t.p)
	if err != nil {
		return storeErr(err), nil, nil
	}
	if replayed {
		return textResult("already queued (same request_id) " + format.TaskLine(task)), nil, nil
	}
	return textResult("queued " + format.TaskLine(task)), nil, nil
}

func (t tools) taskList(ctx context.Context, _ *mcp.CallToolRequest, in TaskListIn) (*mcp.CallToolResult, any, error) {
	var since time.Time
	if in.SinceHours > 0 {
		since = t.st.Now().Add(-time.Duration(in.SinceHours) * time.Hour)
	}
	list, err := t.st.List(ctx, in.State, since)
	if err != nil {
		return storeErr(err), nil, nil
	}
	if len(list) == 0 {
		return textResult("no tasks"), nil, nil
	}
	lines := make([]string, len(list))
	for i, task := range list {
		lines[i] = format.TaskLine(task)
	}
	return textResult(strings.Join(lines, "\n")), nil, nil
}

func (t tools) taskGet(ctx context.Context, _ *mcp.CallToolRequest, in TaskIDIn) (*mcp.CallToolResult, any, error) {
	task, err := t.st.Get(ctx, in.ID)
	if err != nil {
		return storeErr(err), nil, nil
	}
	return textResult(format.TaskDetail(task)), nil, nil
}

func (t tools) taskCancel(ctx context.Context, _ *mcp.CallToolRequest, in TaskIDIn) (*mcp.CallToolResult, any, error) {
	return t.adminOp(ctx, in.ID, "cancelled", t.st.Cancel)
}

func (t tools) taskRequeue(ctx context.Context, _ *mcp.CallToolRequest, in TaskIDIn) (*mcp.CallToolResult, any, error) {
	return t.adminOp(ctx, in.ID, "requeued", t.st.Requeue)
}

// adminOp re-checks the principal even though non-admin servers never list
// these tools.
func (t tools) adminOp(ctx context.Context, id int64, verb string, op func(context.Context, int64) (api.Task, error)) (*mcp.CallToolResult, any, error) {
	if !t.p.Admin {
		return errResult("admin token required"), nil, nil
	}
	task, err := op(ctx, id)
	if err != nil {
		return storeErr(err), nil, nil
	}
	return textResult(verb + " " + format.TaskLine(task)), nil, nil
}
