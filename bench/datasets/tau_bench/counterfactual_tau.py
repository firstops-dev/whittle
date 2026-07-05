#!/usr/bin/env python3
"""
Trace-based LOSSLESS counterfactual for tau-bench.

Replays each task's GROUND-TRUTH action sequence against the real env (no agent/user
LLM needed) to get real tool outputs, compresses each (json_crusher, lossless), and
prices the session with the caching-aware Step-6 model. Because compression is
lossless the agent trajectory is unchanged, so this counterfactual is EXACT, not an
approximation.

Session model per task: fixed prefix (policy + tool schemas, enters at turn 0, cached)
+ tool outputs (each enters at its call turn i, re-read in turns i+1..K). Token entering
at turn i of a K-turn session is weighted W + R*(K-i)  (W=1.25 cache-write, R=0.1 read).
Reported: raw tool-output reduction (intrinsic), and caching-aware cost reduction of the
tool-output-bearing context (excludes agent-reasoning/user-message tokens we don't have —
those would dilute the % somewhat; stated as such).
"""
import sys, types, json, urllib.request
# stub litellm so tau_bench imports without the heavy dep / any API call
lit = types.ModuleType("litellm"); lit.completion = lambda *a, **k: None
sys.modules["litellm"] = lit
sys.path.insert(0, "tau-bench")
import tiktoken

ENC = tiktoken.get_encoding("o200k_base")
tok = lambda s: len(ENC.encode(s or "", disallowed_special=()))
W, R = 1.25, 0.1
API = "http://127.0.0.1:8095/v1/compress"


def compress(c):
    r = urllib.request.urlopen(urllib.request.Request(
        API, data=json.dumps({"content": c}).encode(),
        headers={"Content-Type": "application/json"}, method="POST"), timeout=30)
    return json.loads(r.read())


def leaves(x):
    o = []
    if isinstance(x, dict):
        [o.extend(leaves(v)) for v in x.values()]
    elif isinstance(x, list):
        [o.extend(leaves(v)) for v in x]
    else:
        o.append(str(x))
    return o


def weight(i, K):
    return W + R * (K - i)


def run(domain, limit):
    import importlib
    load_data = importlib.import_module(f"tau_bench.envs.{domain}.data").load_data
    ALL_TOOLS = importlib.import_module(f"tau_bench.envs.{domain}.tools").ALL_TOOLS
    WIKI = importlib.import_module(f"tau_bench.envs.{domain}.wiki").WIKI
    tt = importlib.import_module(f"tau_bench.envs.{domain}.tasks_test")
    TASKS = getattr(tt, "TASKS", None) or getattr(tt, "TASKS_TEST")
    tools_map = {t.get_info()["function"]["name"]: t for t in ALL_TOOLS}
    prefix_tok = tok(WIKI) + tok(json.dumps([t.get_info() for t in ALL_TOOLS]))

    tasks = TASKS[:limit]
    tot_orig = tot_comp = 0
    base_cost = comp_cost = 0.0
    n_out = lossless = 0
    per = []
    for task in tasks:
        data = load_data()
        acts = task.actions if hasattr(task, "actions") else task["actions"]
        outs = []
        for a in acts:
            name = a.name if hasattr(a, "name") else a["name"]
            kw = a.kwargs if hasattr(a, "kwargs") else a["kwargs"]
            if name in ("respond", "think") or name not in tools_map:
                continue
            try:
                o = tools_map[name].invoke(data=data, **kw)
            except Exception as e:
                o = f"Error: {e}"
            outs.append(o)
        if not outs:
            continue
        K = len(outs)
        t_orig = t_comp = 0
        bc = prefix_tok * weight(0, K)   # fixed prefix priced once (enters turn 0)
        cc = prefix_tok * weight(0, K)
        for i, o in enumerate(outs, 1):
            n_out += 1
            oo = tok(o)
            try:
                resp = compress(o)
            except Exception:
                resp = {"action": "skipped"}
            if resp.get("action") == "compressed":
                co = tok(resp.get("compressed") or o)
                if "json_crusher" in (resp.get("strategy") or ""):
                    try:
                        if all(str(l) in resp["compressed"] for l in set(map(str, leaves(json.loads(o))))):
                            lossless += 1
                    except Exception:
                        pass
            else:
                co = oo
            t_orig += oo; t_comp += co
            bc += oo * weight(i, K); cc += co * weight(i, K)
        tot_orig += t_orig; tot_comp += t_comp
        base_cost += bc; comp_cost += cc
        per.append((K, t_orig, t_comp))
    return {
        "domain": domain, "tasks": len(per), "prefix_tok": prefix_tok,
        "tool_outputs": n_out, "lossless": lossless,
        "raw_tool_tok_orig": tot_orig, "raw_tool_tok_comp": tot_comp,
        "raw_reduction_pct": round(100 * (1 - tot_comp / tot_orig), 1) if tot_orig else 0,
        "session_cost_reduction_pct": round(100 * (1 - comp_cost / base_cost), 1) if base_cost else 0,
        "median_tool_calls": sorted(k for k, _, _ in per)[len(per) // 2] if per else 0,
    }


if __name__ == "__main__":
    limit = int(sys.argv[1]) if len(sys.argv) > 1 else 50
    for dom in ("retail", "airline"):
        r = run(dom, limit)
        print(json.dumps(r, indent=1))
