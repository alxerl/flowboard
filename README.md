# Flowboard

Flowboard is a Go and PostgreSQL delivery tracker for small software projects. It combines a task board with GitHub pull request events: opening a PR moves a linked task into review, and merging it marks the task done. The dashboard shows delivery status and average cycle time.

## Run locally

```sh
docker compose up --build
```

Open <http://localhost:8080> and choose **Explore demo workspace**. The demo workspace is created on the first start. Set `DEMO_MODE=0` to disable demo access and start with an empty database. The local Compose stack uses development database credentials; configure your own `DATABASE_URL` for other environments.

To run Go directly, start PostgreSQL and set `DATABASE_URL` as shown in `.env.example`, then run `go run .`.

## GitHub integration

1. Create a project with its repository in `owner/name` form.
2. Set `GITHUB_WEBHOOK_SECRET` to a random secret. For Compose, put it in a local `.env` file.
3. In the GitHub repository, add a webhook pointing to `https://your-host/webhooks/github`, select `application/json`, choose **Pull requests** events, and use the same secret.
4. Include the project key and task ID in a PR title or description, for example `ATL-2 Add dashboard metrics`.

Only signed `pull_request` events are accepted. Duplicate GitHub delivery IDs are ignored. The webhook works only for projects whose repository matches the event repository.

## API

| Method | Path | Purpose |
|---|---|---|
| POST | `/api/auth/register`, `/api/auth/login`, `/api/auth/logout` | Accounts and sessions |
| GET / POST | `/api/projects` | List or create projects |
| GET / POST | `/api/projects/{id}/members` | List members or add an existing user (owner only) |
| GET / POST | `/api/projects/{id}/tasks` | List or create tasks |
| GET / PATCH / DELETE | `/api/tasks/{id}` | Read, update or delete a task |
| GET | `/api/tasks/{id}/events` | Task activity history |
| GET | `/api/projects/{id}/metrics` | Delivery metrics |
| POST | `/webhooks/github` | Receive signed GitHub PR events |

Example:

```sh
curl -X POST http://localhost:8080/api/projects/1/tasks \
  -H 'Content-Type: application/json' \
  -d '{"title":"Ship release dashboard","priority":"high","assignee":"Alex"}'
```

The API uses an HTTP-only session cookie. A new project is owned by its creator. Owners can add registered users as members or owners; only project members can access its tasks, events, and metrics. `DEMO_MODE=1` enables a shared demo account, so use `DEMO_MODE=0` for private workspaces.

## Next milestones

- Email invitations and self-service acceptance for project membership.
- GitHub issue linking and webhook-driven automation rules.
- Activity charts, filters and bottleneck reports.
- Hosted demo and a short walkthrough video.
