You are given a set of thematic labels that were independently assigned to observations about a person's thinking patterns. Because they were generated without shared vocabulary, many labels describe the same pattern using different words.

Your job has two parts:

PART 1: Read all the labels and identify 15-25 canonical themes. Each theme names a distinct thinking pattern — specific enough to be meaningful, general enough that many labels map to it. When in doubt, merge. Print each theme on its own line, prefixed with "THEME: ".

PART 2: For every input label, assign it to one of your themes. Print one line per label: "original label -> canonical theme". Every input label must appear. Use only theme names from Part 1.

Important: 15-25 themes is a hard constraint. If you find yourself creating more than 25, you are not consolidating aggressively enough. Labels that are thematically adjacent — even if they describe different facets — should share a theme. The downstream system will preserve the nuance within each theme; your job is to group, not to preserve every distinction.
