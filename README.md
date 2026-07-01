<div align="center">

<img src="frontend/public/favicon.svg" width="84" alt="Vivid AI" />

<h1>image2api</h1>

**多供应商 AI 生图 / 生视频网关 —— 一套 OpenAI 兼容 API,聚合七大平台,开箱即用的运营系统**

<sub>线上实例(品牌):[Vivid AI · vividai.run](https://vividai.run)</sub>

**简体中文** | [English](README.en.md)

[![Online Demo](https://img.shields.io/badge/在线体验-vividai.run-7c3aed?style=for-the-badge)](https://vividai.run)

[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Vue 3](https://img.shields.io/badge/Vue-3-42b883?logo=vuedotjs&logoColor=white)](https://vuejs.org)
[![Docker](https://img.shields.io/badge/Docker-ready-2496ED?logo=docker&logoColor=white)](#-部署)
[![OpenAI Compatible](https://img.shields.io/badge/OpenAI-compatible-412991?logo=openai&logoColor=white)](#-openai-兼容-api)
[![HTTPS](https://img.shields.io/badge/HTTPS-反代自理-lightgrey)](#-部署)
[![Providers](https://img.shields.io/badge/供应商-7%20平台-orange)](#-支持的模型--供应商)
[![Self-hosted](https://img.shields.io/badge/self--hosted-yes-success)](#-部署)
[![License](https://img.shields.io/badge/license-MIT-blue)](#-license)

[在线体验](https://vividai.run) · [功能](#-核心功能) · [部署](#-部署) · [API 文档](#-openai-兼容-api) · [交流群](#-交流--联系)

<br/>

<img src="docs/screenshots/playground.png" alt="image2api — 画图台" width="860" />

</div>

---

## 📖 目录

- [简介](#-简介)
- [界面预览](#-界面预览)
- [核心功能](#-核心功能)
- [支持的模型 / 供应商](#-支持的模型--供应商)
- [OpenAI 兼容 API](#-openai-兼容-api)
- [部署](#-部署)
- [技术栈](#-技术栈)
- [仓库结构](#-仓库结构)
- [Roadmap](#-roadmap)
- [交流 / 联系](#-交流--联系)
- [License](#-license)

## ✨ 简介

**image2api** 把 Adobe Firefly、OpenAI、Runway、Grok、Leonardo、Krea、Imagine 等平台,以及**任意 OpenAI 兼容上游**的图像 / 视频能力,统一封装成**一套 OpenAI 兼容的 API**;背后用多账号池自动调度 —— 权重优先 + 并发感知、额度耗尽自动换号、认证失效自动刷新或判死、临时错误自动重试、token 到期前主动续期 —— 对外提供稳定服务。

它不只是 API 代理:自带**积分计费、CDK 充值、邀请奖励、用户体系、管理后台、现代化画图前端**,一条命令即可跑成一个对外运营的 AI 生成站点 —— 作者的线上实例 **[Vivid AI · vividai.run](https://vividai.run)**(品牌)即基于本项目搭建。

> 💡 前后端**完全开源**(MIT),Go + Vue 3,可自由二开 / 自部署。

**一句话亮点** 🔌 OpenAI 兼容 · 🤖 7 平台十余模型 · 🔁 自动换号 / Token 保活 · 💳 积分 + 在线充值(易支付)+ 代理价 · 🧩 并发分组 · 🎨 画图前端 + 管理后台 · 🐳 一键部署(TLS 反代自理)

## 🖼️ 界面预览

| 画图台 | 概览看板 |
|:---:|:---:|
| ![画图台](docs/screenshots/playground.png) | ![概览看板](docs/screenshots/dashboard.png) |
| **账号管理** | **日志** |
| ![账号管理](docs/screenshots/accounts.png) | ![日志](docs/screenshots/logs.png) |

## 🚀 核心功能

#### 🎨 生成能力
- 生图 + 生视频一站式,支持**图生图 / 参考图**(首帧、末帧、风格参考)
- 多分辨率(图像 1K / 2K / 4K · 视频 720p / 1080p)、多宽高比、视频多时长,按模型独立配置与定价
- 7 大供应商、十余模型,后台**动态启用 / 下架 / 改价**,无需改代码

#### 🔌 OpenAI 兼容
- 文生图 `/v1/images/generations` · 图生图 `/v1/images/edits`(multipart 上传参考图) · 视频 `/v1/videos`(Sora 式异步:创建→轮询→`/content` 下载) · `/v1/models`
- **严格 OpenAI 入参**:`size` **同时决定比例 + 分辨率档**(图像看长边 → 1K/2K/4K,视频看短边 → 720p/1080p),改个 `base_url` + `api_key` 即接现有 OpenAI SDK
- 图片结果 **base64 直返**,服务端不留存文件,隐私友好;站内 **/docs** 附「分辨率对照表」直接查 `size` 该传什么

#### 🔁 多账号池 + 智能故障转移
- 账号池调度,单账号出错不影响整体
- **权重优先 + 并发感知**:按账号权重从高到低调度,某账号并发满了才轮到下一个;同权重组内 round-robin 均摊。每账号并发数可配(上游账号),其余系统固定
- **额度耗尽→换号** · **认证失效→刷新重试 / 判死** · **临时错误→同号重试 ×3** · **参数错→直接报错**
- **预扣额度**:生成前原子扣减,失败自动退回,杜绝并发超额

#### 🔗 自定义上游聚合(OpenAI 兼容)
- 把任意 **OpenAI 兼容的 v1 端点**当成一个账号接入(填 `base_url` + `key`),无需写代码
- **按 model id 自动路由**:上游声明支持哪些 id,生成该 id 时即走对应上游(可覆盖内置 provider);id 留空 = 全部
- 模型管理里自由新建自定义模型(id / 类型 / 比例 / 分辨率·价 / 时长·价 / 参考图),按本地价计费
- 调用**直连不走代理**;上游可配权重与并发,与内置池统一调度

#### 🔐 Token 自动保活
- 一次性轮换 token(Krea / Imagine)**到期前 10 分钟主动续期**,新 token 自动落库
- Adobe cookie 定时换 token;纯 JWT 到期自动判死
- 每日额度按平台重置时间自动恢复 + 重新探测真实余额

#### 💳 计费与运营
- 积分制(**预扣 + 失败退款**),按模型 / 分辨率 / 时长精细定价
- **代理价体系**:用户可设为「代理」角色,模型可设代理价;代理用户(含其 API Key 调用)自动按代理价计费,未设代理价则回退普通价
- **在线充值(易支付)**:微信 / 支付宝扫码,金额档位 + 自定义,订单 30 分钟未付自动取消,支付回调 MD5 验签 + 幂等自动到账;累计充值可查
- **CDK 兑换码**充值 · **邀请奖励** · 邮箱注册 / 验证码 / 找回密码
- **并发分组**:按分组限制用户「同时生成数」(画图台 + API Key 合计,`0` = 不限),Redis 自愈计数,新用户自动入默认组
- 三级角色:普通用户 / 代理 / 管理员(唯一)

#### 🖥️ 用户前台(Vue 3)
- 画图台 · 创作记录画廊 · 生成日志(含失败原因 / 来源标签)
- **充值 · 订单**(充值记录 / 未付可继续支付) · API 文档 · API Key 管理 · 邀请 · 关于,亮 / 暗主题
- **站内公告**:登录后自动弹出 Markdown 公告,内容更新即重推

#### 🛠️ 管理后台
- 概览看板(趋势 / DAU / 失败 Top / 消费榜)
- 模型管理(普通价 + 代理价) · 账号管理(批量导入 / 去重 / 额度) · **并发分组** · **订单管理**(筛选 / 搜索 / 分页) · 全站日志 · 用户管理(设为代理 / 分配并发组 / 看累计充值) · CDK · 展示位 · **站点公告** · 站点配置(含易支付)

**🧰 工程亮点**:tls-client(Chrome JA3/JA4 指纹)稳定穿透 Cloudflare · 媒体存 S3/RustFS 经鉴权代理分发 + 保留期清理 · 自愈式维护轮询(恢复额度 / 刷新凭据 / 清理僵死任务并退款) · 一条命令 Docker 部署(TLS 交给你的反代)。

## 🤖 支持的模型 / 供应商

| 供应商 | 模型(示例) | 类型 |
|---|---|---|
| **Adobe Firefly** | firefly-image-5 · firefly-gpt-image-2 · flux-kontext-max · firefly-video · firefly-ray · gemini-veo31 | 图像 / 视频 |
| **OpenAI** | gpt-image-2 | 图像 |
| **Runway** | runway-gen4-turbo · nano-banana-2(Nano Banana 2) | 视频 / 图像 |
| **Grok（grok.com）** | grok-video（imagine 文生 / 图生视频) | 视频 |
| **Leonardo.ai** | seedream-4.5 | 图像 |
| **Krea.ai** | flux-klein-2 | 图像 |
| **Imagine.art** | imagine-1.5 · imagine-1.5pro | 图像 |
| **自定义上游** | 任意 OpenAI 兼容 v1 端点(按 id 路由) | 图像 / 视频 |

> 模型由管理后台动态启用并定价,可随时增删。自定义上游支持把任何 OpenAI 兼容服务接成账号,按 model id 路由调用。

## 🔌 OpenAI 兼容 API

```bash
# 文生图 —— 纯 OpenAI 参数:size 同时决定比例 + 分辨率档(长边 <1800→1K / <3500→2K / ≥3500→4K)
curl https://你的域名/v1/images/generations \
  -H "Authorization: Bearer sk-xxxx" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-2",
    "prompt": "a cute cat on a desk, studio lighting",
    "size": "2048x2048"
  }'

# 图生图 —— multipart 上传参考图(可多张 image[])
curl https://你的域名/v1/images/edits \
  -H "Authorization: Bearer sk-xxxx" \
  -F model="seedream-4.5" -F prompt="改成赛博朋克风格" -F image=@input.png
```

图片返回 OpenAI 风格 `{ "created": ..., "data": [{ "b64_json": "..." }] }`(原始 base64,无 `data:` 前缀,服务端不留存)。**视频**走异步:`POST /v1/videos` 建任务 → 轮询 `GET /v1/videos/{id}` 至 `completed` → `GET /v1/videos/{id}/content` 取 mp4。完整参数见站内 **/docs** 文档页。

## 🚀 部署

> 域名 + HTTPS 由你自己的反向代理处理(本项目不内置证书签发)。

**Docker(推荐)**:`docker compose up -d --build` 一条命令拉起 PostgreSQL + Redis + RustFS + 后端 + 前端(nginx **HTTP 监听容器 2000 端口**),把你的反向代理指到 `http://<本机>:2000`(端口用 `WEB_PORT` 改;要改密码 / 密钥 / `CORS_ORIGINS`(反代走 HTTPS 时把 `COOKIE_SECURE` 设为 `true`),直接改 `docker-compose.yml` 里对应值即可)。

也可**从源码手动构建**,自备 **PostgreSQL · Redis · RustFS(或任意 S3)· 反向代理**:

```bash
# 1. 创建空库(后端启动自动建表)
createdb vivid_ai

# 2. 配置并从源码构建后端
cat > backend/.env <<'EOF'
APP_ENV=production
HTTP_ADDR=127.0.0.1:6666
POSTGRES_DSN=host=127.0.0.1 user=postgres password=你的密码 dbname=vivid_ai port=5432 sslmode=disable TimeZone=Asia/Shanghai
REDIS_ADDR=127.0.0.1:6379
RUSTFS_ENDPOINT=http://127.0.0.1:9000
RUSTFS_BUCKET=vivid-ai
RUSTFS_ACCESS_KEY=你的AK
RUSTFS_SECRET_KEY=你的SK
CORS_ORIGINS=https://你的域名
COOKIE_SECURE=true
EOF
cd backend && go build -o bin/api ./cmd/api && ./bin/api   # 监听 127.0.0.1:6666

# 3. 构建前端(产物 frontend/dist)
cd frontend && npm install && npm run build
```

Nginx 反代(证书自行用 certbot / acme.sh):

```nginx
server {
    listen 443 ssl;
    server_name 你的域名;
    ssl_certificate     /path/fullchain.pem;
    ssl_certificate_key /path/privkey.pem;
    root /path/to/frontend/dist;
    index index.html;
    client_max_body_size 50m;
    proxy_read_timeout 600s;            # 视频生成耗时长

    location /assets/ { expires 1y; add_header Cache-Control "public, max-age=31536000, immutable"; }
    location / { try_files $uri $uri/ /index.html; add_header Cache-Control "no-cache"; }
    location ^~ /admin/api/ { proxy_pass http://127.0.0.1:6666; }
    location ^~ /images/    { proxy_pass http://127.0.0.1:6666; }
    location = /health      { proxy_pass http://127.0.0.1:6666; }
    location ^~ /v1/        { proxy_pass http://127.0.0.1:6666; add_header Cache-Control "no-store" always; }
}
```

> 完整环境变量见 `backend/.env.example`。

## 🧱 技术栈

| 层 | 技术 |
|---|---|
| 后端 | Go · gin · gorm(PostgreSQL)· go-redis · tls-client(Chrome 指纹) |
| 前端 | Vue 3 · Vue Router · Vite · Tailwind CSS v4 |
| 基础设施 | PostgreSQL · Redis · RustFS(S3 兼容)· Nginx |

## 📦 仓库结构

```
backend/                       后端源码(Go)
├── cmd/
│   ├── api/                   服务入口(main)
│   └── marklabel/             运维小工具(按需标记账号)
├── internal/
│   ├── bootstrap/             应用装配、定时维护任务启动
│   ├── config/                环境变量配置加载
│   ├── http/
│   │   ├── handler/           HTTP 处理器(v1 兼容接口、后台、鉴权…)
│   │   ├── middleware/        鉴权 / 请求 ID 等中间件
│   │   └── router/            路由注册
│   ├── model/                 GORM 数据模型
│   ├── provider/              各上游供应商客户端
│   │   ├── adobe/             Adobe Firefly(tls-client 指纹)
│   │   ├── chatgpt/           OpenAI(含 PoW / turnstile)
│   │   ├── runway/            Runway 视频 + Nano Banana 图像
│   │   ├── grok/              Grok(grok.com,statsig 伪造,视频)
│   │   ├── leonardo/          Leonardo
│   │   ├── krea/              Krea
│   │   ├── imagine/           Imagine.art
│   │   ├── custom/            自定义上游(OpenAI 兼容 v1,按 id 路由,直连不走代理)
│   │   └── epay/              易支付(mapi 下单 + 回调 MD5 验签,积分充值)
│   ├── repo/                  数据访问层(用户 / 模型 / 账号 / 日志 / CDK / 订单 / 并发组…)
│   ├── service/              业务逻辑(生成调度、计费、账号池、保活、维护)
│   └── storage/               RustFS / S3 媒体存储
├── Dockerfile                 多阶段构建(源码编译 → 精简运行镜像)
└── .env.example               后端环境变量模板

frontend/                      前端源码(Vue 3 + Vite)
├── src/
│   ├── views/                 页面(画图台 / 账号 / 模型 / 用户 / 并发组 / 订单 / 日志 / 概览 / 充值 / 设置…)
│   ├── components/             复用组件(弹窗 / 选择器 / 灯箱…)
│   ├── layouts/                公共 / 后台布局
│   ├── utils/                  工具函数
│   └── api.js · auth.js …      接口封装、鉴权、主题、积分等
├── Dockerfile                 Nginx 静态托管(HTTP :2000)+ API 反代
└── default.conf.template      Nginx 站点模板(反代 + 缓存策略)

docker-compose.yml             Docker 编排(Postgres / Redis / RustFS / 后端 / 前端)
.env.example → backend/.env    后端环境变量模板
```

## 🗺️ Roadmap

- [ ] 更多上游供应商接入
- [ ] 用量统计 / 导出
- [ ] 多语言界面(i18n)
- [ ] Webhook / 异步回调

## 💬 交流 / 联系

| | |
|---|---|
| 🌐 官网 | **[vividai.run](https://vividai.run)** |
| 👥 QQ 交流群 | **1106849765** · [点击加群](https://qm.qq.com/q/976LeMFoHu) |
| 🐧 QQ | **1114639355** · [加好友](https://qm.qq.com/q/ItgCcNA7ac) |
| 🛒 小店 | **[pay.ldxp.cn/shop/chiyi](https://pay.ldxp.cn/shop/chiyi)** |
| ✉️ 邮箱 | vividairun@gmail.com |

## ⭐ Star History

<!-- 建好 GitHub 仓库后,取消下面这行注释并把 OWNER 换成你的用户名即可显示趋势图: -->
<!-- [![Star History Chart](https://api.star-history.com/svg?repos=OWNER/image2api&type=Date)](https://star-history.com/#OWNER/image2api&Date) -->

觉得有用就点个 ⭐ 吧 —— 建好仓库后取消上方注释即可展示 Star 趋势图。

## 📄 License

本项目(前端 + 后端)基于 [MIT](LICENSE) 协议开源,可自由使用、修改、商用与二次分发。

<div align="center">

如果这个项目对你有帮助,欢迎 ⭐ Star 支持!

</div>
