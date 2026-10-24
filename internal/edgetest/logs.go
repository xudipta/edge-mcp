package edgetest

import (
	"fmt"
	"strings"
)

const crashTrace = `2026-10-23T11:58:41Z INFO  defect-detector starting (model=defect-v3.onnx, accelerator=gpu.intel.com/i915)
2026-10-23T11:58:41Z INFO  loading model from /models/defect-v3.onnx
2026-10-23T11:58:42Z ERROR failed to load model: open /models/defect-v3.onnx: no such file or directory
2026-10-23T11:58:42Z ERROR model sync from registry.pune.local timed out after 30s (last error: dial tcp 10.20.0.5:5000: i/o timeout)
Traceback (most recent call last):
  File "/app/main.py", line 42, in <module>
    detector = Detector.load(MODEL_PATH)
  File "/app/detector.py", line 27, in load
    session = ort.InferenceSession(path, providers=PROVIDERS)
onnxruntime.capi.onnxruntime_pybind11_state.NoSuchFile: [ONNXRuntimeError] : 3 : NO_SUCHFILE : Load model from /models/defect-v3.onnx failed
`

// Logs returns canned logs for the sample fleet: healthy pods log a normal
// startup, and the crash-looping defect-detector-c explains why it keeps dying.
func Logs(namespace, pod, container string, previous bool) (string, error) {
	if namespace == "vision" && pod == "defect-detector-c" {
		return crashTrace, nil
	}
	if previous {
		return "", fmt.Errorf("previous terminated container %q in pod %q not found", "app", pod)
	}
	app := strings.TrimSuffix(pod[:strings.LastIndex(pod, "-")+1], "-")
	var b strings.Builder
	fmt.Fprintf(&b, "2026-10-23T11:00:00Z INFO  %s starting\n", app)
	b.WriteString("2026-10-23T11:00:01Z INFO  model loaded, accelerator ready\n")
	b.WriteString("2026-10-23T11:00:01Z INFO  listening on :8080\n")
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(&b, "2026-10-23T11:%02d:00Z INFO  processed 120 frames, p95 latency 38ms\n", i*10)
	}
	return b.String(), nil
}
