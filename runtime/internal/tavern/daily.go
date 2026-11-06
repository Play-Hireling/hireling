package tavern

import (
	"context"
	"net/url"
	"time"
)

// PlanItem is one task line of the daily plan.
type PlanItem struct {
	Task     string `json:"task"`
	Title    string `json:"title"`
	Owner    string `json:"owner"`
	Priority string `json:"priority"`
	Status   string `json:"status"`
	Goal     string `json:"goal,omitempty"`
}

// PlanOwner is one hireling's part of the day.
type PlanOwner struct {
	Agent string     `json:"agent"`
	Tasks []PlanItem `json:"tasks"`
	More  int        `json:"more,omitempty"`
}

// DailyPlan is tavern-core's record of one day's plan (B10,
// services/tavern-core/internal/daily.Plan).
type DailyPlan struct {
	Day       string      `json:"day"`
	TZ        string      `json:"tz"`
	PostedBy  string      `json:"posted_by"`
	PostedAt  time.Time   `json:"posted_at"`
	Quest     string      `json:"quest,omitempty"`
	Owners    []PlanOwner `json:"owners"`
	Waiting   []PlanItem  `json:"waiting,omitempty"`
	Approvals []PlanItem  `json:"approvals,omitempty"`
	Summary   string      `json:"summary"`
	Text      string      `json:"text"`
	// Commentary is the Innkeeper's note from its morning turn; LoggedAt is
	// set once the plan reached the log (unset = the runtime still owes it).
	Commentary   string     `json:"commentary,omitempty"`
	CommentaryAt *time.Time `json:"commentary_at,omitempty"`
	LoggedAt     *time.Time `json:"logged_at,omitempty"`
}

// Tasks counts the open tasks the plan schedules.
func (p *DailyPlan) Tasks() int {
	n := 0
	for _, o := range p.Owners {
		n += len(o.Tasks) + o.More
	}
	return n
}

// PostDailyPlan is POST /plans/daily (B10): tavern-core composes the day's
// plan from the queue and the quest and keeps it once per day. created is
// false when the day already had its plan (the runtime restarted), so the
// caller does not log it twice.
func (c *Client) PostDailyPlan(ctx context.Context, day, tz string) (*DailyPlan, bool, error) {
	var out struct {
		Created bool       `json:"created"`
		Plan    *DailyPlan `json:"plan"`
	}
	if err := c.post(ctx, "/plans/daily", map[string]string{"day": day, "tz": tz}, &out); err != nil {
		return nil, false, err
	}
	return out.Plan, out.Created, nil
}

// CommentDailyPlan is POST /plans/daily/{day}/commentary: the Innkeeper's
// note on the day's plan, kept once (a retry returns the stored plan).
func (c *Client) CommentDailyPlan(ctx context.Context, day, text string) (*DailyPlan, error) {
	var p DailyPlan
	if err := c.post(ctx, "/plans/daily/"+url.PathEscape(day)+"/commentary", map[string]string{"text": text}, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// MarkDailyLogged is POST /plans/daily/{day}/logged: the plan is in the log.
func (c *Client) MarkDailyLogged(ctx context.Context, day string) error {
	return c.post(ctx, "/plans/daily/"+url.PathEscape(day)+"/logged", map[string]string{}, nil)
}
