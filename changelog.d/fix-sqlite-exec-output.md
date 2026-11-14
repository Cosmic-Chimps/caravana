### Fixed

- A SQLite backup or restore is no longer recorded as failed ("the size of the copy is unreadable") when
  Nomad reports a step's end before its output arrives. Each step also writes its result to a file in the
  copy, and Caravana reads that file when the output is missing.
