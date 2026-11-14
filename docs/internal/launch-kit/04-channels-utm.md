# 渠道、UTM 与发布节奏

## 1. 目标与漏斗

冷启动阶段只看四个数，不看 star：

| 阶段 | 指标 | 4 周目标（建议） | 采集方式 |
| --- | --- | --- | --- |
| 触达 | 仓库/文档访问 | 500–1,500 | GitHub Traffic + 文档站分析 |
| 尝试 | quickstart 启动 | 30–80 | 文档 CTA 的 UTM + 反馈 issue |
| 成功 | 首次问答完成 | 15–40 | quickstart_check 通过 / 用户反馈 |
| 留存 | 7 日内再次访问或提问 | 5–15 | 文档站回访、Discussions |

目标按“首月无付费推广”设定；达不到时优先改首屏与 quickstart，而不是加渠道。

## 2. 渠道清单

| 渠道 | 角色 | 形式 | 频率 | 备注 |
| --- | --- | --- | --- | --- |
| GitHub | 转化落地页 | README、Release、Discussions、topics | 持续 | 所有渠道最终导到这里 |
| 掘金 | 中文技术主阵地 | 机制文（文章 1/2/3） | 每周 1 篇 | 标题问题化，正文给证据 |
| 知乎 | 认知与对比 | 长文、问答 | 每两周 1 篇 | 不贬低竞品，只讲差异 |
| V2EX | 冷启动反馈 | `/go/share` 或 `/go/create` | 首发 1 次 + 重大版本 | 标题写清“开源/自托管” |
| 微信公众号 | 长文沉淀 | 文章 + 演示视频 | 每两周 | 文末统一 CTA |
| B 站 | 降低理解成本 | 8 分钟演示、直播答疑 | 每月 1 次 | 字幕必需 |
| Gitee / OSChina | 国内镜像 | 仓库镜像 + 版本同步 | 每个 Release | 保留 Issues 指向 GitHub |
| Hacker News | 英文首发 | Show HN | 仅重大版本 | 标题遵守 Show HN 规则 |
| Reddit | 英文社区 | r/selfhosted、r/LocalLLaMA | 每月 1 次 | 先回答问题再放链接 |
| dev.to | 英文长文 | 文章 1/3 的英文版 | 每两周 | 标注 cross-post |
| MCP 目录 | 生态获客 | mcp.so、Smithery 等 | 发布后提交 | 用 MCP 配置页截图与工具列表 |
| awesome lists | 长尾 | awesome-selfhosted、awesome-rag | 发布后 PR | 遵守各列表收录标准 |

## 3. GitHub 仓库优化清单

- [ ] About：一句话定位 + 文档站 + topics：`rag` `llm` `knowledge-base` `self-hosted` `multi-tenant` `pgvector` `mcp`
- [ ] Social preview：1280×640（见 `01-readme-hero.md`）
- [ ] README 首屏：定位、三条命令、差异点、v0.2.0、边界
- [ ] Release 置顶：v0.2.0，附 SBOM/签名/平台二进制说明
- [ ] Discussions 分类：`Q&A`、`Show your setup`、`Ideas`、`Announcements`
- [ ] Issue 模板：bug（含版本/日志/复现）、feature（含场景/边界）、docs
- [ ] `good first issue` 至少 5 个：文档修正、示例语料、SDK 示例、前端文案
- [ ] 英文 README 或顶部英文摘要
- [ ] SECURITY、CONTRIBUTING、CODE_OF_CONDUCT 入口可见

## 4. UTM 规范

统一小写、连字符、不使用空格：

| 参数 | 取值 |
| --- | --- |
| `utm_source` | `github` `juejin` `zhihu` `v2ex` `wechat` `bilibili` `hn` `reddit` `devto` `mcpso` |
| `utm_medium` | `social` `article` `video` `community` `directory` |
| `utm_campaign` | `v020-launch` `evidence-chain` `migration` `presets` `monthly` |
| `utm_content` | 文章序号或视频镜头，如 `post-1` `demo-8min` |

示例：

```text
https://hzc19970630.github.io/rag-platform/?utm_source=juejin&utm_medium=article&utm_campaign=evidence-chain&utm_content=post-1
https://github.com/hzc19970630/rag-platform?utm_source=bilibili&utm_medium=video&utm_campaign=v020-launch&utm_content=demo-8min
```

注意：GitHub 不保证把 UTM 透传到仓库分析；以文档站 UTM + GitHub referrer 组合判断。每次发布前用表格登记链接，避免渠道间串号。

## 5. 发布日 Runbook

**T-1**
- 固定 README、Release、文章草稿与视频；所有链接用 UTM 生成并互测
- 在全新环境跑 `quickstart.sh`，记录耗时与失败点
- 准备 10 条 FAQ（模型、许可、OCR、HA、数据安全、升级、迁移、MCP、SDK、对比）

**D0（按北京时间 10:00 / 美东 09:00 两个窗口）**
1. 发布 README 与 Release 置顶
2. 掘金首发文章 1；V2EX 发“分享”帖；知乎同步
3. B 站上传 8 分钟视频；公众号定时
4. HN Show HN（英文标题，正文写清问题、差异与边界）
5. Reddit 先参与讨论，再发结构化介绍

**D+1**
- 2 小时内回复所有 issue/评论；把高频问题补进文档
- 记录 quickstart 失败点，能当天修就发 patch release

**D+7**
- 复盘漏斗：访问→尝试→首问→留存
- 保留前两名渠道，暂停低转化渠道
- 发布第二篇文章，更新 README 的 FAQ

## 6. 社区运营节奏

- Issue 首响：工作日 24 小时内；不能修也给复现步骤与临时绕过
- 每周一次 `good first issue` 整理；合并外部 PR 时写清行为变化
- 每月一次公开 Roadmap 更新（`ROADMAP.md` + Discussions）
- 每月一次直播/语音答疑，录播剪成 2 分钟片段
- 设计伙伴：10 个免费深度支持名额，换取公开案例；不承诺商业条款与行业包交付时间

## 7. 指标记录表（每次发布复制一份）

| 日期 | 渠道 | 链接（含 UTM） | 访问 | quickstart 启动 | 首问完成 | issue 数 | 备注 |
| --- | --- | --- | ---: | ---: | ---: | ---: | --- |
|  |  |  |  |  |  |  |  |

## 8. 红线

- 不刷 star、不买量、不伪装用户案例
- 不宣称“企业 GA”“零幻觉”“最强 RAG”
- 不在许可结论前宣传 OEM/商业分发
- 不泄露客户语料、租户名、密钥或内网地址
- 竞品对比只引用 `docs/comparison.md` 的文档级差异，并标注未实测
