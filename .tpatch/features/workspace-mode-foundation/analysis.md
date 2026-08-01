# Analysis

Current path helpers are global functions that infer the sibling external workspace layout directly. Adding checkout mode by branching inside each command would risk regressions across every lifecycle path. The safe first slice is an explicit workspace model and backend boundary that preserves external behavior exactly, with characterization tests proving all current resolution priorities and paths.

Compatibility: backward compatible. Existing configs without `mode` resolve to `external`. This slice does not enable checkout lifecycle behavior.

Risks: accidental mode inference from `.tws/config.yaml`, changes to `TwsRoot` precedence, unstable workspace identity, and premature capability abstraction.
