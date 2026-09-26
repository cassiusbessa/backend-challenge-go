package suiteenv

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

// Scrape is one read of /metrics of the process under test, indexed by the
// name of the family, so a case asks for the value of one series by name and
// labels instead of parsing the text itself.
type Scrape map[string]*dto.MetricFamily

// ScrapeMetrics reads /metrics of the process listening at base and parses it
// the way the Prometheus would.
func ScrapeMetrics(ctx context.Context, base string) (Scrape, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/metrics", nil)
	if err != nil {
		return nil, fmt.Errorf("build the scrape: %w", err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("scrape the process: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, res.Body)
		return nil, fmt.Errorf("scrape the process: status %d", res.StatusCode)
	}
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(res.Body)
	if err != nil {
		return nil, fmt.Errorf("parse the scrape: %w", err)
	}
	return families, nil
}

// Value answers the value of the sample of that family whose labels are
// exactly the ones given, and reports whether there is one. A counter and a
// gauge both answer their value; a family the scrape does not carry, or a
// label set no sample has, answers false.
func (s Scrape) Value(name string, labels map[string]string) (float64, bool) {
	family, ok := s[name]
	if !ok {
		return 0, false
	}
	for _, sample := range family.GetMetric() {
		if labelsOf(sample) == nil && len(labels) == 0 || maps.Equal(labelsOf(sample), labels) {
			return valueOf(sample), true
		}
	}
	return 0, false
}

// Labels answers the label names and values of every sample of the family, so
// a case walks the closed set of values a family carries.
func (s Scrape) Labels(name string) []map[string]string {
	family, ok := s[name]
	if !ok {
		return nil
	}
	var out []map[string]string
	for _, sample := range family.GetMetric() {
		out = append(out, labelsOf(sample))
	}
	return out
}

func labelsOf(sample *dto.Metric) map[string]string {
	if len(sample.GetLabel()) == 0 {
		return nil
	}
	out := make(map[string]string, len(sample.GetLabel()))
	for _, label := range sample.GetLabel() {
		out[label.GetName()] = label.GetValue()
	}
	return out
}

func valueOf(sample *dto.Metric) float64 {
	if sample.GetCounter() != nil {
		return sample.GetCounter().GetValue()
	}
	return sample.GetGauge().GetValue()
}
