# Local Setup Guide: Jitsi Meet + Excalidraw + TURN + JWT Auth

This repository contains the complete configuration to run a local, production-like instance of Jitsi Meet. It includes a custom Persistent Excalidraw whiteboard (backed by PostgreSQL), external TURN server routing (Coturn), and JWT-based authentication to manage room creation and user privileges.

Our custom infrastructure is pre-configured in the `docker-compose.override.yml` and Coturn configuration files included in this repository.

---

## ⚙️ Prerequisites
Before starting, ensure you have the following installed on your machine:
* **Docker Desktop:** Ensure it is running. *(Windows Users: Make sure the "Use WSL 2 based engine" option is enabled in Docker settings).*
* **Git Bash:** *(Windows Users only)* You must use Git Bash to run the password generation scripts. Standard Command Prompt or PowerShell will not work.

---

## 🚀 Step-by-Step Setup

### 1. Clone the Repository
Open your terminal (or Git Bash) and clone this repository to your local machine:
```bash
git clone <your-repository-url>
cd <your-repository-folder>
```
### 2. Generate Jitsi Security Passwords
Jitsi requires secure internal passwords for its microservices to talk to each other. We use Jitsi's provided script to generate these locally.
```bash
cp env.example .env
./gen-passwords.sh
```
### 3. Configure Your .env File
You need to make edits to your newly created .env file to enable all of our custom features. Open the .env file in your preferred text editor.

#### A. Set your Local URL:
Find the PUBLIC_URL variable (usually near the top) and set it to localhost:
```
PUBLIC_URL=https://localhost:8443
```
#### B. Append Advanced Configurations:
Scroll to the very bottom of the .env file and append the following configuration blocks. These variables are required for the whiteboard, TURN server, and Auth to initialize correctly:
```
# ==========================================
# CUSTOM JITSI INFRASTRUCTURE CONFIGURATION
# ==========================================

# --- 1. WHITEBOARD & DATABASE CONFIGURATION ---
ENABLE_WHITEBOARD=1
WHITEBOARD_COLLAB_SERVER_PUBLIC_URL=http://localhost:8080
EXCALIDRAW_DB_USER=excalidraw_user
EXCALIDRAW_DB_PASS=local_dev_password_123

# --- 2. EXTERNAL TURN SERVER (COTURN) ---
TURN_HOST=learnforge.com 
TURN_PORT=3478
TURNS_PORT=5349
TURN_TRANSPORT=tcp 
TURN_CREDENTIALS=your_super_secure_auth_secret_here

# --- 3. JWT AUTHENTICATION ---
ENABLE_AUTH=1
AUTH_TYPE=jwt
JWT_APP_ID=learnforge_local_dev
JWT_APP_SECRET=local_dev_jwt_secret_999
```
### 4. Spin Up the Stack
We have two Docker stacks to run: the Coturn server and the Jitsi/Excalidraw stack.

#### A. Start Coturn:
Navigate to the coturn server and run this command in yout terminal:

```bash
docker compose up -d coturn
```
#### B. Start Jitsi & Excalidraw:
From your main Jitsi directory, run:

```bash
docker compose up -d
```
Note: Wait about 60–90 seconds for the database to initialize and Jitsi's services to handshake.

### 🔑 How to Test (Generating a Local JWT)
Because we enabled JWT Authentication, you can no longer simply visit localhost:8443 and type a room name to join as a moderator. You need a token.

For local testing, you can manually generate a token using jwt.io:

1. Go to jwt.io.

2. In the Algorithm dropdown, ensure HS256 is selected.

3. In the Payload (Data) section, paste this JSON test payload:

```json
{
  "aud": "learnforge_local_dev",
  "iss": "learnforge_local_dev",
  "sub": "meet.jitsi",
  "room": "*",
  "context": {
    "user": {
      "name": "Local Developer",
      "email": "dev@learnforge.com",
      "moderator": "true"
    },
    "features": {
      "screen-sharing": "true"
    }
  }
}
```
4. In the Verify Signature section, enter the secret from your .env file: local_dev_jwt_secret_999.

5. Copy the generated encoded token from the left panel.

### Joining the Room
To join your local meeting as an authenticated moderator, append the token to your URL like this:
https://localhost:8443/MyTestRoom?jwt=<PASTE_YOUR_TOKEN_HERE>

🛑 Shutting Down
To stop the servers and free up your system resources, run:

```bash
docker compose down
```
(Note: Running docker compose down will keep your Docker volumes intact. Your whiteboard drawings will still be there the next time you spin the stack up).
