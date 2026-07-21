---
name: deploy_to_vps
description: Deploy the latest pushed changes to the VPS by pulling the git repository, recompiling, and restarting the bot service.
---

# Deploy to VPS

When you are asked to use this skill, perform the deployment immediately using the `finland-mcp-server` server's `execute-command` tool. We now have two bot instances running on this single server. You can assume the user has already committed and pushed their changes.

Based on the server structure, you must update both bot instances:

### Instance 1: Primary Bot
1. **Directory**: Located at `/opt/xui-end-bot`.
2. **Commands**:
```bash
cd /opt/xui-end-bot && git pull && /usr/bin/go build -o bot_linux ./cmd/bot && systemctl restart xui-end-bot.service && sleep 2 && systemctl status xui-end-bot.service --no-pager
```

### Instance 2: Germany Bot
1. **Directory**: Located at `/opt/xui-end-bot-germany`.
2. **Commands**:
```bash
cd /opt/xui-end-bot-germany && git pull && /usr/bin/go build -o bot_linux ./cmd/bot && systemctl restart xui-end-bot-germany.service && sleep 2 && systemctl status xui-end-bot-germany.service --no-pager
```

**Complete One-Liner to update both:**
```bash
(cd /opt/xui-end-bot && git pull && /usr/bin/go build -o bot_linux ./cmd/bot && systemctl restart xui-end-bot.service && sleep 2 && systemctl status xui-end-bot.service --no-pager) && (cd /opt/xui-end-bot-germany && git pull && /usr/bin/go build -o bot_linux ./cmd/bot && systemctl restart xui-end-bot-germany.service && sleep 2 && systemctl status xui-end-bot-germany.service --no-pager)
```

