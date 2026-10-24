"""Calls the real `edge-mcp --demo` server and saves the tool output the demo clip is built from."""
import json, os, subprocess, sys
OUT = os.environ.get("DEMO_OUT", "/tmp/edge-mcp-demo")
os.makedirs(OUT, exist_ok=True)
BIN = os.environ.get("EDGE_MCP_BIN", "bin/edge-mcp")
p = subprocess.Popen([BIN, "--demo"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
def send(m): p.stdin.write(json.dumps(m)+"\n"); p.stdin.flush()
def recv(): return json.loads(p.stdout.readline())
send({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"demo","version":"0"}}}); recv()
send({"jsonrpc":"2.0","method":"notifications/initialized"})
calls = [("cluster_overview",{}),("list_edge_nodes",{"unhealthy_only":True}),("describe_edge_node",{"name":"blr-02"}),
         ("list_workloads",{"unhealthy_only":True}),("get_pod_logs",{"namespace":"vision","pod":"defect-detector-c","previous":True,"tail_lines":4}),
         ("accelerator_usage",{})]
out={}
for i,(n,a) in enumerate(calls, start=2):
    send({"jsonrpc":"2.0","id":i,"method":"tools/call","params":{"name":n,"arguments":a}})
    r=recv(); out[n]={"args":a,"result":r["result"].get("structuredContent")}
json.dump(out, open(os.path.join(OUT, "data.json"),"w"), indent=1)
p.stdin.close(); p.wait(timeout=5)
print("saved", os.path.join(OUT, "data.json"))
