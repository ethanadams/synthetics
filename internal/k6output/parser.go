package k6output

import (
	"bufio"
	"encoding/json"
	"os"
	"time"
)

// K6Metric represents a k6 metric output line
type K6Metric struct {
	Type   string                 `json:"type"`
	Metric string                 `json:"metric"`
	Data   map[string]interface{} `json:"data"`
}

// MetricPoint represents a parsed metric point
type MetricPoint struct {
	Time   time.Time
	Metric string
	Value  float64
	Tags   map[string]string
}

// ParseJSONOutput parses k6 JSON output file and extracts metric points
func ParseJSONOutput(path string) ([]MetricPoint, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var points []MetricPoint
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		var metric K6Metric
		if err := json.Unmarshal(scanner.Bytes(), &metric); err != nil {
			// Skip lines that aren't valid JSON
			continue
		}

		// We're interested in "Point" type metrics
		if metric.Type == "Point" {
			point := MetricPoint{
				Metric: metric.Metric,
				Tags:   make(map[string]string),
			}

			// Extract time
			if timeValue, ok := metric.Data["time"].(string); ok {
				t, err := time.Parse(time.RFC3339Nano, timeValue)
				if err == nil {
					point.Time = t
				}
			}

			// Extract value
			if value, ok := metric.Data["value"].(float64); ok {
				point.Value = value
			}

			// Extract tags
			if tags, ok := metric.Data["tags"].(map[string]interface{}); ok {
				for k, v := range tags {
					if strVal, ok := v.(string); ok {
						point.Tags[k] = strVal
					}
				}
			}

			points = append(points, point)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return points, nil
}

// GroupMetricsByName groups metric points by metric name for easier processing
func GroupMetricsByName(points []MetricPoint) map[string][]MetricPoint {
	grouped := make(map[string][]MetricPoint)
	for _, point := range points {
		grouped[point.Metric] = append(grouped[point.Metric], point)
	}
	return grouped
}

// CalculateStats calculates basic statistics for a set of metric values
func CalculateStats(values []float64) map[string]float64 {
	if len(values) == 0 {
		return nil
	}

	var sum float64
	min := values[0]
	max := values[0]

	for _, v := range values {
		sum += v
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}

	return map[string]float64{
		"count": float64(len(values)),
		"sum":   sum,
		"avg":   sum / float64(len(values)),
		"min":   min,
		"max":   max,
	}
}
