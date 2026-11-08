#!/usr/bin/env python3
"""Add domains / IPs to the sing-box proxy rules, validate, restart.

usage: sudo ./sbadd.py "a.com, *.b.com, *gmail.com, 1.2.3.4, 10.0.0.0/8"
       ./sbadd.py -c config.json --no-restart a.com
"""
import argparse, ipaddress, json, re, shutil, subprocess, sys

def classify(e):
    try:
        return "ip_cidr", str(ipaddress.ip_network(e, strict=False))
    except ValueError:
        pass
    e = e.lower()
    if not re.fullmatch(r"[a-z0-9.*-]+", e) or "." not in e.replace("*", ""):
        sys.exit(f"bad entry: {e!r}")
    if e.startswith("*.") and "*" not in e[2:]:
        e = e[2:]
    if "*" not in e:
        return "domain_suffix", e  # matches the domain and all subdomains
    return "domain_regex", "^" + re.escape(e).replace(r"\*", ".*") + "$"

def rule_for(rules, key):
    for r in rules:
        if key in r and r.get("outbound") == "gw":
            return r
    r = {key: [], "outbound": "gw"}
    rules.append(r)
    return r

ap = argparse.ArgumentParser()
ap.add_argument("entries", nargs="+")
ap.add_argument("-c", "--config", default="/etc/sing-box/config.json")
ap.add_argument("--no-restart", action="store_true")
a = ap.parse_args()

entries = [x for s in a.entries for x in re.split(r"[,\s]+", s) if x]
cfg = json.load(open(a.config))
rules = cfg["route"]["rules"]
for e in entries:
    key, val = classify(e)
    lst = rule_for(rules, key)[key]
    if val not in lst:
        lst.append(val)
        print(f"+ {key}: {val}")
    else:
        print(f"= {key}: {val} (already there)")

shutil.copy(a.config, a.config + ".bak")
json.dump(cfg, open(a.config, "w"), indent=2)
r = subprocess.run(["sing-box", "check", "-c", a.config])
if r.returncode:
    shutil.copy(a.config + ".bak", a.config)
    sys.exit("check failed, config restored")
if not a.no_restart:
    subprocess.run(["systemctl", "restart", "sing-box"], check=True)
    print("restarted sing-box")
