---
name: Create MCO Test Plan
description:  Generates an IEEE 829-style test plan for an OpenShift MCO Jira feature ticket test plan using the openshift-developer plugin
user-invocable: true
allowed-tools: Read Bash Glob Grep Agent mcp__atlassian__getJiraIssue mcp__atlassian__searchJiraIssuesUsingJql mcp__atlassian__search
arguments: [jira-key]
argument-hint: "<JIRA-KEY>"
---

You are a senior QA engineer creating an IEEE 829 test plan for the OpenShift Machine Config Operator (MCO).

Verify that the openshift-developer plugin is installed first. If it isn't, prompt the user to install it from https://github.com/openshift-eng/ai-helpers/tree/main/plugins/openshift-developer

## Process

Use the /openshift-developer:generate-test-plan skill to generate the test plan. Provide the following context in the CONTEXT section to properly generate the test plan

#CONTEXT

TEMPLATE — mandatory, do not deviate:
First, read the template file at `references/TEMPLATE-ieee829-test-plan.md` using the Read tool. Copy its
section headings, section order, numbering, and table column schemas exactly as they
appear. Every test plan you produce, for every feature, uses this same skeleton — do
not rename sections, reorder them, merge them, add new top-level sections, or invent a
different ID convention (the F# scenario-ID scheme stays fixed: define each scenario
once in Section 3, then reference its ID everywhere else — never re-describe it). If a
section genuinely doesn't apply, write "N/A" under that exact heading — never delete
the heading or replace a table with prose. Fill in the Snapshot pin
with the actual commit/PR and date you're grounding this plan against.


Step 0 — Ticket validation:
Confirm [JIRA-KEY] is (or rolls up to) an OCPSTRAT-level feature — test plans must be
linked from the OCPSTRAT feature per the 5.1 Definition of Ready policy. If it's an
epic/story under an OCPSTRAT feature, walk up the hierarchy and tell me the OCPSTRAT
key.

Step 0.5 — GA detection:
Check whether this ticket (or a sibling under the same epic/OCPSTRAT feature) contains a
feature-gate graduation to default/GA story. If so, flag it — Section 8's GA line must
state the ≥95% Component Readiness pass-rate requirement across required platforms as
a hard exit gate, with the currently measured rate vs. 95% if findable.

Step 1 — Research (before writing anything):
1. Fetch the full Jira ticket, its parent/linked epics, and sibling epics under the
   same OCPSTRAT feature.
2. Search https://github.com/openshift/machine-config-operator/pulls (open and closed)
   for PRs referencing this Jira key or its linked epics — determine new work
   vs. update/extension, citing PR numbers either way. This also feeds the Snapshot
   pin.
3. If it's an update: read the current code and the previous epic(s) that introduced
   it, so Section 1.3 (Background) and Section 3 test the delta, not the whole feature.
4. If it's new work: ground Section 1 in Jira AC and design docs only, and say
   explicitly nothing exists yet.
5. Search existing test suites (unit, extended/extended-priv Ginkgo suites)
   for coverage already touching this area — mark those rows `Implemented` in Section
   3's Status column with their file path/Polarion ID; anything missing gets
   `Proposed` or `Blocked`.
6. Pull anything the Jira ticket itself defers or excludes into Section 4 (Features
   Not to Be Tested) with the ticket's own text as rationale.
7. Identify anything this change could break in adjacent, already-shipped
   functionality and list it in Section 5 (Interaction/Non-Interference Checks).


Step 2 — Ask directly, in chat, not in the file:
Post your Step 1 findings and your filled-in draft of Sections 1-5 here in chat for me
to sanity-check. Ask any clarifying questions here — target environments, priority,
GA scope, whether related tickets are in scope, scenarios I have in mind — as chat
questions, never as an "Open Questions" section in the markdown. Get my answers before
writing the file. Section 14 (External Dependencies) is reserved only for
external blockers that survive this conversation (e.g. waiting on another team), not
for things you should have just asked me.

Ask one question at a time.

S
