# 二开版本的页面更新

本 fork 的默认更新仓库为 `wells974/sub2api`。管理员仍使用原有的「检查更新 → 立即更新 → 立即重启」流程；检查更新和历史版本回滚均从同一个仓库读取。

## 更新来源

服务启动时读取 `UPDATE_GITHUB_REPO=owner/repository`。不填写时使用本 fork 的默认值；填写错误会报错，不会自动回退到原项目。切换仓库后，旧仓库的版本缓存不再使用。

当前发布包下载适用于公开 GitHub Releases。`UPDATE_GITHUB_TOKEN` 仅用于 GitHub API 的鉴权和额度，下载附件仍为匿名请求；它不代表支持私有仓库附件。不要将 token 写入源码或提交到仓库。

## 第一次将线上原版切换到本 fork

线上已经运行 `ranxi2001/sub2api` 时，不需要重新安装数据库或重新初始化系统。现有旧程序的更新来源是写死的，不能仅通过修改 `.env` 或点击旧页面的更新按钮切换仓库；需要先替换一次应用镜像。此步骤会短暂重启应用。

1. 在开发电脑上提交二开代码，按后文的发布步骤推送到 `wells974/sub2api`，等待完整 Release 工作流成功。确认自己的 GHCR 镜像可被服务器拉取，Release 同时包含平台程序包和 `checksums.txt`。
2. 在服务器上备份数据库和配置，进入原部署目录，沿用原 Compose 项目名、文件、`.env`、数据库/Redis 配置及数据挂载。不要拿本地的整套配置覆盖线上配置，也不要执行 `docker compose down -v`。
3. 在原 Compose 文件中，只修改应用服务的镜像，并向原有 `environment` 列表添加下面两项。以下是局部片段，不是完整配置；`2.19.9.1` 仅为示例，请替换成你已成功发布且高于线上版本的版本号。

```yaml
services:
  sub2api:
    image: ghcr.io/wells974/sub2api:2.19.9.1
    environment:
      - UPDATE_GITHUB_REPO=wells974/sub2api
      - SUB2API_PERSIST_UPDATES=true
      # 保留原有的所有环境变量、端口和数据挂载
```

4. 使用原来的 Compose 文件和项目参数，只拉取并重建应用服务。下面假设文件名为 `docker-compose.local.yml`、服务名为 `sub2api`；若线上不同，按原部署替换，并保留原有 `-p` / `--env-file` 参数。

```bash
docker compose -f docker-compose.local.yml pull sub2api
docker compose -f docker-compose.local.yml up -d --no-deps --no-build --pull never sub2api
docker compose -f docker-compose.local.yml ps sub2api
docker compose -f docker-compose.local.yml logs --tail=100 sub2api
```

5. 确认应用健康、页面版本正确且原账号和配置可用。后续发布更高版本后，就可以在页面检查更新、安装和重启，不再重复这次切换。

## 可选：在有源码的开发环境中首次安装

如果还没有发布镜像，也可以先在有完整源码的开发环境中构建并安装一次，验证更新源和启动脚本。线上环境优先使用前面的已发布镜像方式。

从仓库根目录执行：

```bash
docker compose -f deploy/docker-compose.local.yml -f deploy/docker-compose.fork.yml build sub2api
docker compose -f deploy/docker-compose.local.yml -f deploy/docker-compose.fork.yml up -d --no-deps --no-build --pull never sub2api
```

这个覆盖配置使用 `ghcr.io/wells974/sub2api` 镜像名，并开启 `SUB2API_PERSIST_UPDATES=true`。如果旧的 `deploy/.env` 已设置 `UPDATE_GITHUB_REPO`，请改为 `wells974/sub2api`。以后管理这个应用容器时继续使用两个 Compose 文件。

首次启动把镜像内的程序复制到 `/app/data/runtime/sub2api`，后续网页更新替换该文件，并在同一目录保存 `.backup`。这使更新程序及备份随已有数据卷保留，重建容器时仍然生效。每个实例应使用自己的数据目录。

开启此选项后，持久化的程序优先于镜像里的程序，**更换镜像也不会覆盖它**。如需改为镜像管理版本，在覆盖配置中关闭 `SUB2API_PERSIST_UPDATES` 后重建应用容器；原持久化程序会保留，重新开启后仍会使用它。不要删除整个数据目录。更换操作系统或 CPU 架构时，应使用对应平台的程序和单独的数据迁移方案。

此方式更新的是应用程序及嵌入的前端。启动脚本、系统依赖和容器基础镜像的修改，仍需重新部署镜像。

## 发布自己的新版本

现有 `.github/workflows/release.yml` 可直接在自己的仓库运行，它按当前仓库发布 Releases 和 GHCR 镜像。不要将二开更新推送到 `ranxi2001/sub2api`。

继续使用原来的 `VERSION` 文件，不增加个人前缀或额外版本文件。三段 `vX.Y.Z` 保留给所基于的上游 / fork 版本，自用二开版本统一使用四段 `vX.Y.Z.N`，例如基于 `v2.19.9` 发布 `v2.19.9.1`，后续二开修订递增为 `v2.19.9.2`。上游标签仍然完整保留，用来选择合并哪个发行版本；将它们映射为本地的 `upstream/vX.Y.Z`，方便区分更新来源。当前 checkout 的上游 remote 名为 `origin`，首次配置如下：

```bash
git config --local remote.origin.tagOpt --no-tags
git config --local --add remote.origin.fetch 'refs/tags/*:refs/tags/upstream/*'
git fetch origin
```

`--no-tags` 禁用默认的同名标签获取，而显式的 fetch 映射负责获取上游标签；两者应配合使用。这项配置只对当前克隆有效，重新克隆后需重新设置；已经配置时不要重复 `--add`。若上游 remote 叫 `upstream`，将配置中的 `remote.origin` 换成 `remote.upstream`。

查看上游发行版本，并按标签定位代码：

```bash
git tag --list 'upstream/*' --sort=-version:refname
# 将 vX.Y.Z 替换成列表中要合并的上游版本
git rev-parse 'refs/tags/upstream/vX.Y.Z^{commit}'
git log --oneline HEAD..refs/tags/upstream/vX.Y.Z
# 确认目标版本和改动后，在自己的开发分支上合并
git merge refs/tags/upstream/vX.Y.Z
```

例如上游的 `v3.0.0` 在本地叫 `upstream/v3.0.0`，基于它的自用二开版本从 `v3.0.0.1` 开始发布。此配置不会删除或改写已经存在的普通标签，已有同名标签仍不能复用。不要使用 `git fetch --tags` 绕过映射，也不要用 `git push --tags` 将 `upstream/*` 等标签批量推送；只向自己的仓库推送本次发布的具体标签。

1. 将全部待发布修改提交到本地 Git；确认推送目标是 `wells974/sub2api`。本地分支名无需与远程相同。
2. 将提交推送到自己的仓库，然后创建一个新的 `v主版本.次版本.修订号.二开修订号` 标签，并将标签推送到同一仓库。标签必须对应包含此次二开修改的提交。
3. 首次在 fork 的 Actions 页面启用工作流。使用完整 Release 流程：`simple_release=false`，仓库变量 `SIMPLE_RELEASE` 也不能设为 `true`。仅发布镜像的 simple 模式无法供页面更新。
4. 等待 Release 工作流成功，确认发布为正式版本（非 draft、非 prerelease），并检查附件含所需平台的 `sub2api_<版本>_linux_amd64.tar.gz` / `sub2api_<版本>_linux_arm64.tar.gz` 以及 `checksums.txt`。
5. 在已安装本 fork 的页面上点击检查更新，再点击立即更新及立即重启。

更新检查和历史回滚按四段数字依次比较，同一上游版本只需递增第四段，例如 `2.19.9.2` 高于 `2.19.9.1`；兼容已有三段版本，缺少的第四段按 `0` 处理。比较逻辑忽略 `-xxx` 后缀，不能仅把同一个版本改成 `-custom1` 来触发更新。Release 构建流程会同步版本号、嵌入前端并设置 `BuildType=release`，使页面显示安装按钮。

下面使用独立的 `fork` remote，避免更改已有 `origin`。这些是发布命令示例，**本次代码修改本身不会执行推送或发布**：

```bash
git remote add fork https://github.com/wells974/sub2api.git
# 若 fork 已存在，先用 git remote get-url fork 核对地址，无需重复添加。
git push fork HEAD:production

# 示例：确认 v2.19.9.1 尚未使用，且大于你正在运行的版本后再执行。
git tag -a v2.19.9.1 -m '发布二开版本及自定义页面更新'
git push fork v2.19.9.1
```

仅在本地提交代码，或只推送分支而没有发布 Release，都不会产生页面可安装的新版本。发布失败时应修复工作流；不要仅手工创建一个没有构建附件的 Release。

## 本地验证

```bash
cd backend
go test -tags unit internal/service/update_service.go internal/service/update_service_test.go internal/service/update_service_repository_test.go internal/service/update_service_install_test.go
cd ..
python3 -m unittest discover -s deploy/tests -p test_persistent_updates.py -v
```

入口脚本测试使用临时目录、无网络的独立容器及假程序，不启动实际应用、不连接现有数据库。默认测试镜像为 `alpine:3.21`，可用 `SUB2API_ENTRYPOINT_TEST_IMAGE` 指定本地已有的兼容镜像。
