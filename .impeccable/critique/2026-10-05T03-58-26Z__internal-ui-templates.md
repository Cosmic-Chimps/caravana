---
target: console pages (internal/ui)
total_score: 24
max_score: 40
na_heuristics: 
p0_count: 0
p1_count: 1
target_identity: "file:/Users/jjchiw/Documents/cosmic-chimps/a__cc/caravana/internal/ui/templates"
timestamp: 2026-10-05T03-58-26Z
slug: internal-ui-templates
---
Method: dual-agent (A: design review · B: detector). Score 24/40 (Acceptable), all 10 heuristics applied.

Priority issues:
- [P1] A failed deploy is unexplained, has no fix, and the apps headline still says healthy. The job-conflict cause is mapped to "the Nomad job changed while deploying" (contradicts its detail line); "Caravana" is capitalised; no action; "deploy failed" cell is not a link. Fix: own sentence for job_conflict, one action, headline "deploy needs you", problems first. (clarify, harden)
- [P2] Delete confirmation label uppercases the app name ("TYPE BLOG TO CONFIRM") while the match is case-sensitive lowercase. Fix: text-transform none on the name span. (polish)
- [P2] Apps and copies lists lose column meaning at 390/768 px; the sidebar switch at 768 leaves ~470 px. Fix: per-cell labels in the stacked layout, switch at ~900 px. (adapt)
- [P2] Inconsistent page structure: Settings centred at 720 px, Deploy missing from the top bar only on Deployments, Configure is 11 mostly-default rows ending in "use the API". (layout, distill)
- [P3] No filter/shortcuts on /apps; no focus on the first aria-invalid field after an htmx swap; revoke popover is an absolute overlay inside an overflow:hidden panel (latent clip).

Detector: 9 design-system-color findings on templates are false positives (CSS not resolved). Browser pass: /settings clipped-overflow-container (real, latent), password form without a username field (real, minor); text-overflow/text-occlusion false positives (closed details). Injection blocked by CSP; overlay only visible with a harness-side CSP bypass.

Strengths: voice and plain words; shape-plus-colour status; deliberate responsive behaviour.
