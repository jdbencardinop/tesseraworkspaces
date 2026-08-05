# Analysis

The checkout lifecycle release omitted the documented enable/mode commands and stores feature metadata directly under `.tws/<feature>` instead of `.tws/features/<feature>`. This makes internal directories indistinguishable from features and blocks a safe session implementation. Because v1.2.2+ users may already have legacy data, the repair must support legacy discovery and a deliberate migration path without silently losing metadata.
