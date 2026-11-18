package audit

import (
	"fmt"

	"github.com/mxschmitt/playwright-go"

	"github.com/nyimbi/afford/internal/model"
)

// focusProbeJS describes the focused element and whether its focus is visible:
// it compares the styles a ring can use with the element focused and blurred.
const focusProbeJS = `
() => {
	let el = document.activeElement;
	let opaque = false;
	// Tab moves focus into a frame: judge the control inside it. A frame we
	// cannot read (another origin) is opaque, and says nothing about a trap.
	while (el && el.tagName === 'IFRAME') {
		let inner = null;
		try { inner = el.contentDocument && el.contentDocument.activeElement; } catch (_) { inner = null; }
		if (!inner) { opaque = true; break; }
		if (inner === el.contentDocument.body) break;
		el = inner;
	}
	if (!el || el === document.body || el === document.documentElement) return null;
	const props = (e) => {
		const s = getComputedStyle(e);
		return [s.outlineStyle, s.outlineWidth, s.outlineColor, s.boxShadow, s.borderColor,
			s.backgroundColor, s.textDecorationLine, s.color].join('|');
	};
	const focused = props(el);
	const outlineOff = getComputedStyle(el).outlineStyle === 'none' || parseFloat(getComputedStyle(el).outlineWidth) === 0;
	el.blur();
	const blurred = props(el);
	el.focus({focusVisible: true});
	const r = el.getBoundingClientRect();
	const name = (el.getAttribute('aria-label') || el.innerText || el.value || el.getAttribute('title') || el.id || '').trim().replace(/\s+/g, ' ').slice(0, 80);
	return {
		tag: el.tagName.toLowerCase(), name, id: el.id || '',
		indicator: focused !== blurred || !outlineOff,
		key: el.tagName.toLowerCase() + '|' + (el.id || '') + '|' + name + '|' + Math.round(r.x) + ',' + Math.round(r.y),
		visible: r.width > 0 && r.height > 0,
		inDialog: !!el.closest('[role=dialog],[aria-modal=true],dialog'),
		opaque
	};
}
`

type focusState struct {
	Tag       string `json:"tag"`
	Name      string `json:"name"`
	ID        string `json:"id"`
	Indicator bool   `json:"indicator"`
	Key       string `json:"key"`
	Visible   bool   `json:"visible"`
	InDialog  bool   `json:"inDialog"`
	Opaque    bool   `json:"opaque"`
}

// Keyboard tabs through the page and reports controls the keyboard cannot
// reach, focus that gets stuck, and focus with no visible indicator. wanted is
// the visible, enabled, non-negative-tabindex interactive controls from the scan.
//
// The pass starts from the top of the document and presses Tab at most
// len(wanted)+8 times (capped at 120): every control gets a chance and a trap
// is detected by the budget running out with the same elements cycling.
func Keyboard(page playwright.Page, pageURL string, wanted []model.Element) []model.Finding {
	budget := len(wanted) + 8
	if budget > 120 {
		budget = 120
	}
	if len(wanted) == 0 {
		return nil
	}
	_, _ = page.Evaluate("() => { window.scrollTo(0,0); if (document.activeElement) document.activeElement.blur(); }")
	order := []focusState{}
	counts := map[string]int{}
	var stuck string
	prevKey := ""
	repeat := 0
	for i := 0; i < budget; i++ {
		if err := page.Keyboard().Press("Tab"); err != nil {
			return nil
		}
		st, err := eval[*focusState](page, focusProbeJS)
		if err != nil || st == nil {
			continue
		}
		if st.Opaque {
			prevKey, repeat = "", 0
			continue
		}
		if st.Key == prevKey {
			repeat++
			if repeat >= 3 {
				stuck = st.Name
				break
			}
		} else {
			repeat = 0
		}
		prevKey = st.Key
		counts[st.Key]++
		if counts[st.Key] == 1 {
			order = append(order, *st)
		}
		// A full cycle: focus is back at the first stop. The page has been
		// walked once; anything not seen is unreachable.
		if len(order) > 1 && st.Key == order[0].Key {
			break
		}
	}

	var out []model.Finding
	if stuck != "" || (len(order) > 0 && len(order) < len(wanted)/2 && counts[order[len(order)-1].Key] > 2) {
		f := model.NewFinding("E_KEYBOARD_TRAP", pageURL)
		f.Element = stuck
		f.Evidence = []string{fmt.Sprintf("Tab stopped advancing after %d distinct stops; the page has %d interactive controls", len(order), len(wanted))}
		f.Detail["stops"] = len(order)
		f.Detail["controls"] = len(wanted)
		out = append(out, f)
	}
	reached := map[string]bool{}
	for _, st := range order {
		reached[st.Tag+"|"+st.Name] = true
	}
	var unreachable []string
	for _, el := range wanted {
		if reached[el.Tag+"|"+el.Name] {
			continue
		}
		unreachable = append(unreachable, fmt.Sprintf("%s “%s”", el.Tag, el.Name))
	}
	if len(unreachable) > 0 && len(out) == 0 {
		f := model.NewFinding("E_KEYBOARD_UNREACHABLE", pageURL)
		shown := unreachable
		if len(shown) > 8 {
			shown = shown[:8]
		}
		f.Element = shown[0]
		f.Detail["controls"] = shown
		f.Detail["count"] = len(unreachable)
		f.Evidence = []string{fmt.Sprintf("%d of %d controls never received focus from Tab: %v", len(unreachable), len(wanted), shown)}
		out = append(out, f)
	}
	var unmarked []string
	for _, st := range order {
		if st.Visible && !st.Indicator {
			unmarked = append(unmarked, fmt.Sprintf("%s “%s”", st.Tag, st.Name))
		}
	}
	if len(unmarked) > 0 {
		f := model.NewFinding("E_NO_FOCUS_INDICATOR", pageURL)
		shown := unmarked
		if len(shown) > 8 {
			shown = shown[:8]
		}
		f.Element = shown[0]
		f.Detail["controls"] = shown
		f.Detail["count"] = len(unmarked)
		f.Evidence = []string{fmt.Sprintf("%d focused control(s) changed no outline, shadow, border or colour: %v", len(unmarked), shown)}
		out = append(out, f)
	}
	return out
}
