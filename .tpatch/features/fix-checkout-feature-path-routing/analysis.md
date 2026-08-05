# Analysis

The layout repair added FeaturesDir and ResolveFeaturePath but left Workspace.FeaturePath pointing to metadata root directly. New checkout add/import therefore still write legacy `.tws/<feature>`, while ListFeatures expects `.tws/features`. Existing commands inconsistently use FeaturePath and bypass ambiguity handling. This must be corrected before sessions rely on feature inject/stack paths.
