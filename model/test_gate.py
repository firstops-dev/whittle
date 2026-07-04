"""Regression tests for the gate — locks the structured-content leak fixed after
the fidelity eval (47/47 real leaked records were Apollo/HubSpot JSON, Figma JS,
and JSX that the old tool_name='...search'->prose path compressed and corrupted).

Run: python test_gate.py   (plain asserts, no pytest needed)
"""
import gate


def klass(content, tool_name=None, content_type=None):
    return gate.classify(content, tool_name, content_type)[0]


# ---- MUST SKIP: structured content, regardless of tool_name ----
SKIP_CASES = [
    # The actual bug: a JSON body from a tool whose name contains "search".
    ('{"companies": [{"name": "Acme", "domain": "acme.com", "employees": 200}]}',
     "mcp__claude_ai_Apollo_io__apollo_mixed_companies_search", None),
    # HubSpot CRM search returning JSON.
    ('{"results": [{"id": "501", "properties": {"name": "X"}}], "total": 1}',
     "mcp__claude_ai_HubSpot__search_crm_objects", None),
    # JSON array.
    ('[{"a": 1}, {"a": 2}, {"a": 3}]', "anything_search", None),
    # Truncated/partial JSON (no clean parse) still detected.
    ('{"companies": [{"name": "Acme", "domain": "acme.com", "phone": "+1',
     "apollo_search", None),
    # Figma design context: JS const assignment.
    ('const imgRectangle1 = "https://www.figma.com/abc";\nconst w = 240;',
     "mcp__plugin_figma_figma__get_design_context", None),
    # JSX from a paper/get_jsx tool.
    ('"(\\n    <div style={{ alignItems: \'center\', boxSizing: \'border-box\' }}>',
     "mcp__plugin_paper_paper__get_jsx", None),
    # Markup / SVG.
    ('<svg viewBox="0 0 24 24"><path d="M3 3h18v18H3z"/></svg>', "fetch", None),
    # Structured MIME wins even if body sniff is ambiguous.
    ("col1,col2,col3\n1,2,3\n4,5,6", "web_fetch", "text/csv"),
    # tool_name skip-direction still works.
    ("def handler():\n    return 1\n", "Read", None),
]

# ---- MUST COMPRESS: genuine prose, even from search/fetch tools ----
COMPRESS_CASES = [
    ("Nostalgia marketing is having a moment in 2025. Ipsos identifies it as a top "
     "consumer trend, and consumers show higher recall and willingness to spend on "
     "nostalgic ads, though execution still matters.",
     "web_search", None),
    ("The meeting covered the Q3 budget review, the product launch timeline, and "
     "next steps for the enterprise rollout. The team agreed to ship in October.",
     "mcp__granola__get_transcript", None),
    ("Courtney is a Customer Experience Operations contractor at Hootsuite in "
     "Vancouver, with prior roles in CS enablement and operations consulting.",
     "apollo_people_search", None),   # prose summary from a search tool -> compress
]


def main():
    fails = 0
    for c, tn, ct in SKIP_CASES:
        k = klass(c, tn, ct)
        if k != "code_structured":
            print("FAIL (should SKIP): tool=%s got=%s :: %r" % (tn, k, c[:50]))
            fails += 1
    for c, tn, ct in COMPRESS_CASES:
        k = klass(c, tn, ct)
        if k != "prose":
            print("FAIL (should COMPRESS): tool=%s got=%s :: %r" % (tn, k, c[:50]))
            fails += 1
    total = len(SKIP_CASES) + len(COMPRESS_CASES)
    print("%d/%d passed" % (total - fails, total))
    if fails:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
