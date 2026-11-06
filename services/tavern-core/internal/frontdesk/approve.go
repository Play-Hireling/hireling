package frontdesk

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrNotApproved means the decision is unknown (or older than the last 200),
// still open, or was opened by another hireling.
var ErrNotApproved = errors.New("frontdesk: the owner has not answered this hireling's decision")

// Confirm proves the owner's answer before the gate grants a blocked action
// (D1/D3). tavern-core shares its network with every hireling, so the body of
// POST /actions/answer proves nothing on its own; the desk's record does,
// because only the owner token can answer a decision. It returns the option
// the owner chose on decisionID, which deskID must have opened.
func (d *Desk) Confirm(_ context.Context, decisionID, agent, deskID string) (string, error) {
	decisionID, deskID = strings.TrimSpace(decisionID), strings.TrimSpace(deskID)
	if decisionID == "" || deskID == "" {
		return "", fmt.Errorf("%w: decision and desk id are required", ErrNotApproved)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, x := range d.st.Decisions {
		if x.ID != decisionID {
			continue
		}
		if x.From != deskID {
			return "", fmt.Errorf("%w: decision %s was opened by %s, not %s", ErrNotApproved, x.ID, x.From, agent)
		}
		if x.Chosen == "" {
			return "", fmt.Errorf("%w: decision %s is still open", ErrNotApproved, x.ID)
		}
		return x.Chosen, nil
	}
	return "", fmt.Errorf("%w: no decision %s", ErrNotApproved, decisionID)
}
