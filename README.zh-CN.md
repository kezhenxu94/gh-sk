# gh sk

[English](README.md) | 简体中文

一个 [gh](https://cli.github.com) 扩展。GitHub Actions 的 secret 只能写入、无法读回；`gh sk` 把它们（以及 Actions 变量）
保存在 macOS 钥匙串（Keychain）或 [gopass](https://github.com/gopasspw/gopass) 存储中作为唯一可信来源，并推送到 GitHub。

## 安装

```sh
gh extension install kezhenxu94/gh-sk   # 支持 macOS 和 Linux（amd64、arm64）
```

从源码安装：`go build -o gh-sk . && gh extension install .`（目录名必须为 `gh-sk`）。

认证：使用 `gh` 当前登录的账号（或 `GH_TOKEN`）。需要 `repo` 权限，管理组织级 secret 还需要 `admin:org`。

## 作用域（Scope）

| 作用域 | 钥匙串中的“位置” |
|---|---|
| 组织 | `github.com/my-org` |
| 仓库 | `github.com/my-org/my-repo` |
| 环境 | `github.com/my-org/my-repo/env/production` |

命令行中 `github.com/` 前缀可省略。省略作用域时使用当前仓库，解析方式与 `gh` 相同：
依次为 `GH_REPO`、`gh repo set-default`、git remote。

在钥匙串中，每一项都是一个“通用密码”：service = 作用域，account = 名称，种类（kind）=
`github-secret` / `github-variable`，注释 = JSON 元数据（组织可见性、同步时间）。
同一作用域内 secret 与变量不能同名。

## 存储后端

macOS 默认使用钥匙串；Linux 始终使用 gopass。若要在 macOS 上也使用 gopass（例如与 Linux 机器共享 secret），
设置 `GH_SK_STORE=gopass`。两个后端相互独立。使用 `migrate` 在两者之间迁移：它会复制值（包括多行值）、组织可见性和同步状态，
因此自上次推送后未改动的条目仍显示为“ok”；整个过程不落盘，源后端保持不变。

```sh
gh sk migrate --all --from keychain --to gopass   # 或指定作用域；--force 覆盖已有的不同值
```

gh sk 使用 gopass 的 Go 库，因此运行时只需要 `gpg`（同步时还需要 `git`）；`gopass` 命令行工具仅用于初始化和同步。
它读取你的 gopass 配置，因此普通的 `pass` 存储同样可用。以树莓派为例：

```sh
sudo apt install gnupg git gopass
gpg --import my-key.asc                 # 与 Mac 上相同的密钥，或添加一个接收者
gopass clone git@example.com:me/pass.git  # 新建存储则使用 `gopass setup`
```

每个条目为 `gh-sk/<作用域>/<名称>`，内容是包含种类、值、元数据和修改时间的 JSON。
存储是 git 仓库时，每次写入都会提交；使用 `gopass sync` 同步（除非 `core.autopush` 为 false，gopass 每次写入后也会推送）。
在无界面的机器上，请让 gpg-agent 使用终端 pinentry（在 `~/.gnupg/gpg-agent.conf` 中设置 `pinentry-program /usr/bin/pinentry-tty`）。

## 用法

```sh
gh sk set my-org/my-repo NPM_TOKEN                # 隐藏输入提示；也可通过管道传入值
gh sk set DATABASE_URL -b 'postgres://…'           # 当前仓库；-b 的值会留在 shell 历史中
gh sk set my-org ORG_TOKEN --visibility selected --repos api,web
gh sk set my-org/my-repo/env/prod REGION --var
gh sk import my-org/my-repo -f .env               # 批量导入
gh sk ls                                          # 当前仓库：名称与同步状态，从不显示值
gh sk ls --all                                    # 钥匙串中的所有作用域
gh sk get my-org/my-repo NPM_TOKEN

gh sk status my-org/my-repo                       # create / update / ok / drift / untracked
gh sk push my-org/my-repo --dry-run
gh sk push --all --prune                          # 钥匙串中的所有作用域
gh sk pull my-org/my-repo                         # 变量：GitHub -> 钥匙串
gh sk pull -f secrets.env                         # 未跟踪的 secret 名称 -> NAME= 模板；
                                                  # 填写后执行：gh sk import -f secrets.env && rm secrets.env
gh sk rm my-org/my-repo OLD_TOKEN --remote
```

## 在本地使用 secret

```sh
gh sk exec -- npm run dev                         # 以环境变量形式注入当前仓库的 secret 和变量
gh sk exec my-org/my-repo/env/prod -- ./deploy.sh
gh sk export -f .env                              # 生成 NAME='value' 格式的 dotenv 文件；--var 导出变量
```

推荐使用 `exec`：不落盘、支持多行值，并原样传递命令的退出码。钥匙串中的值会覆盖同名环境变量；
每次 `exec` 只加载一个作用域；如需同时注入组织级的项，可嵌套调用，内层作用域覆盖外层，与 GitHub Actions 一致：

```sh
gh sk exec my-org -- gh sk exec -- npm run dev
```

嵌套不考虑组织项的可见性：即使在 Actions 中该仓库看不到某个组织 secret（`selected` 未包含该仓库，或公开仓库遇到 `private`），它仍会被注入。

`export` 写出的是明文，因此文件以 `0600` 权限创建、绝不覆盖已有文件，且不应提交到仓库。
值使用单引号包裹，`gh sk import` 可原样读回；多行值（如 PEM 密钥）无法放入一行 dotenv，会被跳过并给出警告。

## 推送与漂移（drift）

每次推送后，gh sk 会在钥匙串条目上记录同步时间以及 GitHub 返回的 `updated_at`。
下一次 `status`/`push` 时：

- **update**：自上次同步后在钥匙串中被修改（或从未由 gh sk 同步过）。
- **ok**：两边都未改变，跳过。
- **drift**：GitHub 的 `updated_at` 比记录的更新，说明有人在 gh sk 之外修改了它。
  默认跳过（并以非零状态退出），除非使用 `--force`。
- **untracked**：只存在于 GitHub；使用 `--prune` 删除。若某作用域/种类在钥匙串中一项都没有，
  则拒绝清理，以免误删未被管理的仓库。

通过 `gh sk set`/`import` 的修改总能被检测到；在“钥匙串访问”中直接编辑则通过条目的修改时间检测；
用 `gopass edit` 直接编辑则检测不到（修改时间保存在条目内容中），请改用 `gh sk set`。

## 注意事项

- 二进制首次读取某个值时 macOS 会请求钥匙串访问权限（选择“始终允许”）。
  重新编译会改变二进制，因此会再次询问。在“钥匙串访问”中或用 `security add-generic-password -U`
  编辑条目会替换其访问控制列表，下次 `gh sk push` 时会弹窗（并阻塞）请求授权。
- 通过管道传入的值会去掉末尾的一个换行符。
- 空值永远不会到达 GitHub：`set` 拒绝、`import` 跳过、`push` 拒绝发送。
- `import` 会跳过 `#` 注释行，并去掉一对匹配的外层引号；行内 `# 注释` 和 `\n` 等转义会按字面保留。

## 开发

```sh
go test ./...                              # 单元测试与 CLI 测试（内存钥匙串 + 模拟 GitHub）
GHSK_KEYCHAIN_TESTS=1 go test ./...        # 同时在真实钥匙串上运行（会写入并删除测试条目）
GHSK_GOPASS_TESTS=1 go test ./...         # 缺少 gpg 时报错而非跳过（gopass 测试使用临时密钥和存储）
```

CI 为每个 pull request 和推送到 `main` 的提交运行全部测试：在 macOS runner 上运行（包括在临时钥匙串中的钥匙串测试），
并在 Linux 上运行 gopass 测试；
只有测试通过后才会发布 release。
