package frontdesk

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Errors of the grant proof (D1/D3).
var (
	// ErrNotApproved means the decision is unknown (or older than the last
	// 200), still open, has no card, or was opened by another hireling.
	ErrNotApproved = errors.New("frontdesk: the owner has not answered this hireling's decision")
	// ErrDecided means a card was attached after the owner answered: an old
	// yes cannot be reused for new actions.
	ErrDecided = errors.New("frontdesk: the decision is already answered")
	// ErrBound means the decision already carries a card: its actions cannot
	// be swapped while the owner reads it.
	ErrBound = errors.New("frontdesk: the decision already carries a card")
	// ErrCardTooLong means the card's line would not fit whole: a cut line
	// could hide an action that a yes grants, so the card is refused.
	ErrCardTooLong = errors.New("frontdesk: the card names more than the decision can show")
)

// maxCard bounds the line tavern-core adds to a decision's context. The line
// is never cut: a card that does not fit is refused.
const maxCard = 400

// Bind attaches a gate card to decisionID before the owner answers it (D1).
// The decision must be open, opened by deskID, and carry no card yet. The
// desk writes, in its own words, what a yes grants into the context the owner
// reads, and returns the option that grants: the first one offered, never an
// option the hireling names.
func (d *Desk) Bind(ctx context.Context, decisionID, agent, deskID string, actions []string) (string, error) {
	decisionID, deskID = strings.TrimSpace(decisionID), strings.TrimSpace(deskID)
	if decisionID == "" || deskID == "" || len(actions) == 0 {
		return "", fmt.Errorf("%w: decision, desk id and actions are required", ErrNotApproved)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.st.Decisions {
		x := &d.st.Decisions[i]
		if x.ID != decisionID {
			continue
		}
		switch {
		case x.From != deskID:
			return "", fmt.Errorf("%w: decision %s was opened by %s, not %s", ErrNotApproved, x.ID, x.From, agent)
		case x.Chosen != "":
			return "", fmt.Errorf("%w: %s", ErrDecided, x.ID)
		case x.Card != "":
			return "", fmt.Errorf("%w: %s", ErrBound, x.ID)
		case len(x.Options) == 0:
			return "", fmt.Errorf("%w: decision %s offers no option", ErrNotApproved, x.ID)
		}
		card := fmt.Sprintf("Choosing %q grants %s one use of: %s.", x.Options[0], agent, strings.Join(actions, ", "))
		if n := len([]rune(card)); n > maxCard {
			return "", fmt.Errorf("%w: %d actions, %d characters (at most %d)", ErrCardTooLong, len(actions), n, maxCard)
		}
		// The desk's line goes first, so a line the hireling wrote in its own
		// context cannot come after it and read as the last word.
		x.Card = card
		x.Context = strings.TrimSpace(x.Card + "\n\n" + x.Context)
		d.save(ctx)
		return x.Options[0], nil
	}
	return "", fmt.Errorf("%w: no decision %s", ErrNotApproved, decisionID)
}

// Confirm proves the owner's answer before the gate grants a blocked action
// (D1/D3). tavern-core shares its network with every hireling, so the body of
// POST /actions/answer proves nothing on its own; the desk's record does,
// because only the owner token can answer a decision. It returns the option
// the owner chose on decisionID and the option that grants (the first one);
// deskID must have opened the decision and a card must be bound to it.
func (d *Desk) Confirm(_ context.Context, decisionID, agent, deskID string) (chosen, approve string, err error) {
	decisionID, deskID = strings.TrimSpace(decisionID), strings.TrimSpace(deskID)
	if decisionID == "" || deskID == "" {
		return "", "", fmt.Errorf("%w: decision and desk id are required", ErrNotApproved)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, x := range d.st.Decisions {
		if x.ID != decisionID {
			continue
		}
		switch {
		case x.From != deskID:
			return "", "", fmt.Errorf("%w: decision %s was opened by %s, not %s", ErrNotApproved, x.ID, x.From, agent)
		case x.Chosen == "":
			return "", "", fmt.Errorf("%w: decision %s is still open", ErrNotApproved, x.ID)
		case x.Card == "" || len(x.Options) == 0:
			return "", "", fmt.Errorf("%w: decision %s carries no card", ErrNotApproved, x.ID)
		}
		return x.Chosen, x.Options[0], nil
	}
	return "", "", fmt.Errorf("%w: no decision %s", ErrNotApproved, decisionID)
}
