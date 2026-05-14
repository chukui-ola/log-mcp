pipeline {
  agent any

  options {
    timestamps()
    disableConcurrentBuilds()
    buildDiscarder(logRotator(numToKeepStr: '20'))
  }

  environment {
    CGO_ENABLED = '0'
    GOPROXY = 'https://goproxy.cn,direct'
    GOFLAGS = '-trimpath'
    PATH = "/var/data/go/1.24.3/go/bin:${env.PATH}"
    DEPLOY_DIR = '/var/www/slp/log-mcp'
    SUPERVISOR_CONF = '/etc/supervisor/conf.d/log-mcp.conf'
    SERVICE_NAME = 'log-mcp'
    SUDO = 'sudo -n'
    INSTALL = '/usr/bin/install'
    SUPERVISORCTL = '/usr/bin/supervisorctl'
  }

  stages {
    stage('Checkout') {
      steps {
        checkout scm
      }
    }

    stage('Setup Go') {
      steps {
        sh '''
          go env -w GOPROXY=https://goproxy.cn,direct
          go env GOPROXY
        '''
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
          $SUDO $INSTALL -d -m 0755 "$DEPLOY_DIR"
          $SUDO $INSTALL -m 0755 dist/log-mcp "$DEPLOY_DIR/log-mcp"
          $SUDO $INSTALL -m 0644 dist/config.example.json "$DEPLOY_DIR/config.example.json"
          $SUDO $INSTALL -m 0640 dist/config.dev.json "$DEPLOY_DIR/config.json"
          $SUDO $INSTALL -m 0640 dist/config.dev.json "$DEPLOY_DIR/config.dev.json"
          $SUDO $INSTALL -m 0644 deploy/supervisor/log-mcp.conf "$SUPERVISOR_CONF"
          $SUDO $SUPERVISORCTL reread
          $SUDO $SUPERVISORCTL update
          $SUDO $SUPERVISORCTL restart "$SERVICE_NAME"
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
