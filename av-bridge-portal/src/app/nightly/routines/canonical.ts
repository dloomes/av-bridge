// Canonical Room Readiness routine, mirroring docs/nightly-lifecycle-spec.md
// §7.1. Used as the starting template when a customer clicks "From
// standard template" — encodes the VC + audio-loopback flow discussed
// with the product team.
//
// The SIP URI is a placeholder; the customer edits it to point at their
// own loopback bridge. Kept as a string constant here (not fetched) so
// the template is available offline / when the routines list is empty.

export const CANONICAL_ROUTINE = {
  name: "Standard room readiness — VC + audio loopback",
  description:
    "Powers on every device, dials a SIP loopback from the video codec, verifies the mics hear the returning audio, then hangs up and resets the DSP.",
  steps: [
    {
      name: "Power on all room devices",
      type: "power_on",
      target: { scope: "room" },
      timeout_seconds: 120,
      on_failure: "abort",
    },
    {
      name: "Warm-up gap",
      type: "wait",
      duration_seconds: 60,
    },
    {
      name: "Dial test bridge",
      type: "device_command",
      target: { device_type: "vc" },
      command: "dial",
      parameters: { uri: "sip:loopback@customer-provided.example" },
      expected: { call_state: "connected" },
      timeout_seconds: 30,
      on_failure: "abort",
    },
    {
      name: "Let audio play",
      type: "wait",
      duration_seconds: 10,
    },
    {
      name: "Check microphones hear audio",
      type: "check_metric",
      target: { device_type: "dsp" },
      metric: "input_level_dbfs",
      operator: "gt",
      threshold: -40,
      sample_window_seconds: 5,
      on_failure: "continue",
    },
    {
      name: "Hang up",
      type: "device_command",
      target: { device_type: "vc" },
      command: "disconnect",
      on_failure: "continue",
    },
    {
      name: "Reset DSP to default preset",
      type: "device_command",
      target: { device_type: "dsp" },
      command: "recall_preset",
      parameters: { preset: "default" },
    },
  ],
};
