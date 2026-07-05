#!/usr/bin/env python3
"""Regenerates the token-estimator calibration behind compress/tokens.go and the
README MAE claim. Requires tiktoken (pip install tiktoken). Grid-searches the
integer constants of EstimateTokens against o200k_base on 10 content classes and
prints per-class error + MAE. The constants in tokens.go correspond to the BEST
row: (d_word=8, d_digit=3, d_punct=4, sp_div=6)."""
import tiktoken, json, itertools
enc = tiktoken.get_encoding("o200k_base")
T = lambda s: len(enc.encode(s, disallowed_special=()))
samples = {}
samples["prose"] = ("The deployment finished after a long wait and the team gathered to review the results "
  "before deciding on the next steps for the upcoming release. Everyone agreed the migration went smoothly. ")*8
samples["json_min"] = json.dumps([{"name":f"pod-{i}","namespace":"production","status":"Running","restarts":i%3} for i in range(40)],separators=(",",":"))
samples["csv_env"] = "__schema__,name\n"+ "\n".join(f"pod-{i:03d},Running,node-{i%5}" for i in range(60))
samples["go_code"] = 'package main\n\nimport "fmt"\n\nfunc main() {\n\tfor i := 0; i < 100; i++ {\n\t\tresult := compute(i, i*2)\n\t\tfmt.Println(result)\n\t}\n}\n'*10
samples["log"] = "\n".join(f"2026-07-03T10:{i//60:02d}:{i%60:02d}Z INFO request served path=/v1/resource id={i} status=200 latency={i*3}ms" for i in range(60))
samples["padded_table"] = "NAME                          READY   STATUS    RESTARTS   AGE\n"+"\n".join(f"web-{i:02d}-7d9f8c5b-abcde         1/1     Running   0          5d" for i in range(40))
samples["markdown"] = ("# Heading One\n\nSome paragraph text describing the system in detail for readers.\n\n- item one\n- item two\n\n## Section\n\nMore prose here.\n")*10
samples["unicode"] = ("café naïve 日本語のテキスト émojis 🎉 mixed with English words here. ")*20
samples["numbers"] = ",".join(str(i*7919) for i in range(300))
samples["yaml"] = ("apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web\nspec:\n  replicas: 3\n")*15

def estimate(s, d_word, d_digit, d_punct, sp_div):
    toks=0; i=0; n=len(s)
    while i<n:
        c=s[i]
        if c.isascii() and c.isalpha():
            j=i
            while j<n and s[j].isascii() and s[j].isalpha(): j+=1
            toks += 1 + (j-i-1)//d_word; i=j
        elif c.isdigit():
            j=i
            while j<n and s[j].isdigit(): j+=1
            toks += (j-i + d_digit-1)//d_digit; i=j
        elif c in ' \t':
            j=i
            while j<n and s[j] in ' \t': j+=1
            if j-i>1: toks += 1 + (j-i-2)//sp_div
            i=j
        elif c=='\n':
            j=i
            while j<n and s[j]=='\n': j+=1
            toks += 1; i=j
        elif not c.isascii():
            toks += 1; i+=1
        else:
            j=i
            while j<n and s[j].isascii() and not s[j].isalnum() and s[j] not in ' \t\n': j+=1
            toks += (j-i + d_punct-1)//d_punct; i=j
    return toks

best=None
for p in itertools.product([5,6,7,8],[3],[2,3,4],[6,8,10]):
    errs=[abs(estimate(s,*p)-T(s))/T(s) for s in samples.values()]
    mae=sum(errs)/len(errs)
    if best is None or mae<best[0]: best=(mae,max(errs),p)
mae,mx,p=best
print(f"BEST (d_word,d_digit,d_punct,sp_div): {p}  MAE={mae*100:.1f}% max={mx*100:.1f}%")
for k,s in samples.items():
    t=T(s); e=estimate(s,*p)
    print(f"{k:<14}{t:>9}{e:>8}{100*(e-t)/t:>7.1f}%")
