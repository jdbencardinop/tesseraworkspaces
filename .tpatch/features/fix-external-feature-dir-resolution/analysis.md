# Analysis

RequireWorkspace currently calls MainRepoRoot and returns not-inside-git from an external feature directory. Before workspace modes, TwsRoot detected .tws-workspace and allowed commands from workspace/feature paths. The fix must recover the external workspace root from the marker and infer the default source repo safely without weakening checkout mode or silently choosing among ambiguous repos.
