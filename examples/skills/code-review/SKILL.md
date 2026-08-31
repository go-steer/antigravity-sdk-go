---
name: code-review
description: "Review Go and Python code for readability, naming, and style-guide compliance. ACTIVATE this skill when the user asks for a code review, a readability pass, or feedback on a snippet."
---

# Code review

A short, opinionated review pass. Prefer specific, actionable feedback over
general praise.

## Procedure

1. Read the snippet or file the user supplied. If it is longer than 200 lines,
   suggest reviewing one aspect or one file at a time.
2. Check naming first — it is the change with the highest ratio of clarity
   gained to code churn.
3. Check control flow: deep nesting, repeated blocks, and functions that are
   hard to test in their current shape.
4. Report findings shortest-path-to-fix first.

## Style rules

**Go**

- Exported identifiers are `MixedCaps`, unexported ones are `mixedCaps`. Never
  underscores.
- Initialisms keep their case: `URL`, `ID`, `HTTP` — `userID`, not `userId`.
- Errors are values: return them, wrap them with `%w`, do not panic in library
  code.
- Accept interfaces, return structs.

**Python**

- `snake_case` for functions and variables, `CamelCase` for classes.
- Type-annotate public functions.

## What not to flag

- Personal style preferences that do not affect readability.
- Micro-optimizations that make the code harder to follow.
- Formatting a formatter already owns (`gofmt`, `black`).

## Output format

Use a table when there is more than one finding:

| File | Line | Issue | Severity |
| :--- | :--- | :---- | :------- |

Use a diff block when suggesting an edit:

```diff
-func DoThing(x int) int { return x*2 }
+func Double(x int) int { return x * 2 }
```
