<p align="center">
  <img src="assets/flowboard-banner.svg" alt="Flowboard task board" width="100%">
</p>

<p align="center">
  <strong>A task board for software projects.</strong><br>
  Manage tasks, connect GitHub, and see how work is going.
</p>

<p align="center">
  <a href="https://github.com/alxerl/flowboard/actions/workflows/ci.yml"><img src="https://github.com/alxerl/flowboard/actions/workflows/ci.yml/badge.svg" alt="CI status"></a>
  &nbsp; Go · PostgreSQL · Docker
</p>

## What it looks like

This is the sample workspace included with the app.

![Flowboard dashboard with task columns and progress charts](assets/dashboard.png)

## Run it

Install Docker with Compose, then run:

```sh
docker compose up --build
```

Open **[localhost:8080](http://localhost:8080)** and click **Explore demo workspace**. The app creates sample projects and tasks for you. You do not need a GitHub account to try it.

Press `Ctrl+C` to stop. To remove the local database too, run `docker compose down -v`.

## Features

- **Task board:** Move tasks between Backlog, In progress, In review, and Done. Add priorities and assignees.
- **GitHub connection:** GitHub issues become tasks. A pull request linked to a task moves it to review, then to done when merged.
- **Team projects:** Create an account, add teammates, and control who can see each project.
- **Progress view:** See completed tasks, average completion time, and tasks that have been waiting too long.

## Connect your GitHub repository (optional)

The sample workspace works without this setup. To connect your own repository:

1. Create a project and enter its GitHub repository as `owner/name`.
2. Put a secret in `GITHUB_WEBHOOK_SECRET` in a local `.env` file. See `.env.example`.
3. Add a GitHub webhook for **Issues** and **Pull requests**. Use the same secret and set the URL to `https://your-host/webhooks/github`.
4. Put a task ID such as `ATL-2` in a pull request title or description.

Flowboard checks the webhook secret and ignores repeat deliveries.

## Built with

Go serves the API and web interface. PostgreSQL stores projects, tasks, users, and activity. Docker Compose starts both services. GitHub Actions tests the code and checks that the full app starts from a clean checkout.

<details>
<summary><strong>API routes</strong></summary>

| Method | Path | Purpose |
|---|---|---|
| POST | `/api/auth/register`, `/api/auth/login`, `/api/auth/logout` | Accounts and sessions |
| GET / POST | `/api/projects` | List or create projects |
| GET / POST | `/api/projects/{id}/members` | List or add members |
| GET / POST | `/api/projects/{id}/tasks` | List or create tasks |
| GET / PATCH / DELETE | `/api/tasks/{id}` | Read, update or delete a task |
| GET | `/api/tasks/{id}/events` | Task history |
| GET | `/api/projects/{id}/metrics` | Project numbers |
| GET | `/api/projects/{id}/insights` | Charts and delayed tasks |
| POST | `/webhooks/github` | GitHub events |

The demo account is shared. Set `DEMO_MODE=0` and use your own database credentials if you want a private workspace.

</details>
