package quality

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// ExtractMetric reads a performance budget's number from command output: the
// first capture group of the first match of metric. Thousands separators
// (`1,234`) are tolerated; anything else that does not parse as a float is
// an error, never a zero.
func ExtractMetric(metric, output string) (float64, error) {
	re, err := regexp.Compile(metric)
	if err != nil {
		return 0, fmt.Errorf("metric does not compile: %v", err)
	}
	m := re.FindStringSubmatch(output)
	if m == nil {
		return 0, fmt.Errorf("metric %q matched nothing in the command's output", metric)
	}
	if len(m) < 2 {
		return 0, fmt.Errorf("metric %q has no capture group", metric)
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(m[1]), ",", ""), 64)
	if err != nil {
		return 0, fmt.Errorf("metric %q captured %q, which is not a number", metric, m[1])
	}
	return v, nil
}

// BudgetVerdict judges one measured value against a budget's absolute ceiling
// (max, nil for none) and its allowed regression over the previous
// measurement (maxRegressionPct, 0 for none; baseline nil when there is no
// previous measurement). Over either threshold is a fail. With no baseline
// the regression half is recorded as "no baseline yet" -- this measurement
// becomes the baseline -- and does not fail.
func BudgetVerdict(name, unit string, value float64, max *float64, maxRegressionPct float64, baseline *float64) (domain.QualityStatus, string) {
	u := ""
	if unit != "" {
		u = " " + unit
	}
	parts := []string{fmt.Sprintf("%s = %s%s", name, formatNum(value), u)}
	status := domain.QualityPass
	if max != nil {
		if value > *max {
			status = domain.QualityFail
			parts = append(parts, fmt.Sprintf("over the budget of %s%s", formatNum(*max), u))
		} else {
			parts = append(parts, fmt.Sprintf("within the budget of %s%s", formatNum(*max), u))
		}
	}
	switch {
	case baseline == nil && maxRegressionPct > 0:
		parts = append(parts, "no baseline yet (this measurement becomes it)")
	case baseline != nil:
		delta := 0.0
		if *baseline != 0 {
			delta = (value - *baseline) / *baseline * 100
		}
		parts = append(parts, fmt.Sprintf("%+.1f%% vs the previous %s%s", delta, formatNum(*baseline), u))
		if maxRegressionPct > 0 && delta > maxRegressionPct {
			status = domain.QualityFail
			parts = append(parts, fmt.Sprintf("more than the allowed %.1f%% regression", maxRegressionPct))
		}
	}
	if max == nil && maxRegressionPct == 0 {
		// Nothing to judge against: the number is recorded, not graded.
		parts = append(parts, "no threshold declared (recorded only)")
	}
	return status, strings.Join(parts, "; ")
}

func formatNum(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
