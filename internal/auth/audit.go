package auth

import "strings"

// RedactAuditDetails recursively strips credential-bearing metadata before persistence.
func RedactAuditDetails(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		key := strings.ToLower(k)
		sensitive := false
		for _, part := range []string{"password", "secret", "token", "authorization", "cookie", "credential"} {
			if strings.Contains(key, part) {
				sensitive = true
			}
		}
		if sensitive {
			out[k] = "[redacted]"
			continue
		}
		if nested, ok := v.(map[string]any); ok {
			v = RedactAuditDetails(nested)
		}
		if values, ok := v.([]any); ok {
			copyValues := make([]any, len(values))
			for i, value := range values {
				if nested, yes := value.(map[string]any); yes {
					value = RedactAuditDetails(nested)
				}
				copyValues[i] = value
			}
			v = copyValues
		}
		out[k] = v
	}
	return out
}
