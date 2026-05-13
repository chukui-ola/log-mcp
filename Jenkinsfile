pipeline {
  agent any

  options {
    timestamps()
    disableConcurrentBuilds()
    buildDiscarder(logRotator(numToKeepStr: '20'))
  }

  environment {
    CGO_ENABLED = '0'
    GOFLAGS = '-trimpath'
    DEPLOY_DIR = '/var/www/slp/log-mcp'
    SUPERVISOR_CONF = '/etc/supervisor/conf.d/log-mcp.conf'
    SERVICE_NAME = 'log-mcp'
  }

  stages {
    stage('Checkout') {
      steps {
        checkout scm
      }
    }

    stage('Go Version') {
      steps {
        sh 'go version'
      }
    }

    stage('Test') {
      steps {
        sh 'go test ./...'
      }
    }

    stage('Build') {
      steps {
        sh '''
          mkdir -p dist
          go build -ldflags="-s -w" -o dist/log-mcp ./cmd/log-mcp
          cp config.example.json dist/config.example.json
          cp deploy/supervisor/log-mcp.conf dist/log-mcp.supervisor.conf
        '''
      }
    }

    stage('Smoke Test') {
      steps {
        sh '''
          printf '%s\n' \
            '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' \
            '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' \
            | ./dist/log-mcp -config config.example.json

          ./dist/log-mcp -config config.example.json -listen 127.0.0.1:18081 > /tmp/log-mcp-smoke.log 2>&1 &
          pid=$!
          trap 'kill $pid || true' EXIT
          sleep 1
          curl -fsS http://127.0.0.1:18081/healthz
          curl -fsS -X POST http://127.0.0.1:18081/mcp \
            -H 'Content-Type: application/json' \
            -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}'
        '''
      }
    }

    stage('Deploy') {
      when {
        branch 'main'
      }
      steps {
        sh '''
          sudo install -d -m 0755 "$DEPLOY_DIR"
          sudo install -m 0755 dist/log-mcp "$DEPLOY_DIR/log-mcp"
          sudo install -m 0644 dist/config.example.json "$DEPLOY_DIR/config.example.json"
          if [ ! -f "$DEPLOY_DIR/config.json" ]; then
            sudo install -m 0640 dist/config.example.json "$DEPLOY_DIR/config.json"
          fi
          sudo install -m 0644 deploy/supervisor/log-mcp.conf "$SUPERVISOR_CONF"
          sudo supervisorctl reread
          sudo supervisorctl update
          sudo supervisorctl restart "$SERVICE_NAME"
        '''
      }
    }
  }

  post {
    always {
      archiveArtifacts artifacts: 'dist/**', fingerprint: true, onlyIfSuccessful: true
    }
  }
}
