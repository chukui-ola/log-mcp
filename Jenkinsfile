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
          cp config.dev.json dist/config.dev.json
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
        '''
      }
    }

    stage('Deploy') {
      steps {
        sh '''
          sudo install -d -m 0755 "$DEPLOY_DIR"
          sudo install -m 0755 dist/log-mcp "$DEPLOY_DIR/log-mcp"
          sudo install -m 0644 dist/config.example.json "$DEPLOY_DIR/config.example.json"
          sudo install -m 0640 dist/config.dev.json "$DEPLOY_DIR/config.json"
          sudo install -m 0640 dist/config.dev.json "$DEPLOY_DIR/config.dev.json"
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
