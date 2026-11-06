package llm

import "testing"

func TestLooksTooLong(t *testing.T) {
	for _, message := range []string{
		"maximum context length exceeded",
		"prompt is too long",
		"payload too large",
		"文本过长",
	} {
		if !looksTooLong(message) {
			t.Errorf("looksTooLong(%q) = false", message)
		}
	}
	if looksTooLong("invalid api key") {
		t.Error("an auth error must not be classified as too long")
	}
}
