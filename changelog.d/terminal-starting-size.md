### Fixed

- A terminal opened through the API whose first line is typed at once (a script) now runs it at the
  size asked for with `cols` and `rows`, not at 80x24: Caravana passes nothing typed on to Nomad
  before the starting size.
