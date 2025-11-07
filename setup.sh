#!/bin/bash

# Setup script for op-alt-da CI/CD environment

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

# Functions
print_header() {
    echo -e "${CYAN}=================================================${NC}"
    echo -e "${CYAN}    op-alt-da CI/CD Setup Script${NC}"
    echo -e "${CYAN}=================================================${NC}"
    echo
}

print_success() {
    echo -e "${GREEN}✓${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}⚠${NC} $1"
}

print_error() {
    echo -e "${RED}✗${NC} $1"
}

check_command() {
    if command -v "$1" &> /dev/null; then
        print_success "$1 is installed"
        return 0
    else
        print_error "$1 is not installed"
        return 1
    fi
}

install_golangci_lint() {
    echo -e "${CYAN}Installing golangci-lint...${NC}"
    curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(go env GOPATH)/bin v1.55.2
    print_success "golangci-lint installed"
}

install_docker_compose() {
    echo -e "${CYAN}Docker Compose is required but not found.${NC}"
    echo "Please install Docker Compose from: https://docs.docker.com/compose/install/"
}

setup_git_hooks() {
    echo -e "${CYAN}Setting up Git hooks...${NC}"
    
    # Create pre-commit hook
    cat > .git/hooks/pre-commit << 'EOF'
#!/bin/bash
# Pre-commit hook for op-alt-da

echo "Running pre-commit checks..."

# Run linting
if ! make lint; then
    echo "Linting failed. Please fix errors before committing."
    exit 1
fi

# Run tests
if ! make test; then
    echo "Tests failed. Please fix failing tests before committing."
    exit 1
fi

echo "Pre-commit checks passed!"
EOF
    
    chmod +x .git/hooks/pre-commit
    print_success "Git hooks configured"
}

setup_environment() {
    echo -e "${CYAN}Setting up environment...${NC}"
    
    # Create .env file if it doesn't exist
    if [ ! -f .env ]; then
        cat > .env << 'EOF'
# Celestia Configuration
CELESTIA_NAMESPACE=0000000000000000000000000000000000000000000000000000000000000000
CELESTIA_AUTH_TOKEN=

# Logging
LOG_LEVEL=INFO
LOG_FORMAT=json

# S3 Configuration (Optional)
S3_CREDENTIAL_TYPE=
S3_BUCKET=
S3_PATH=
S3_ENDPOINT=
S3_ACCESS_KEY_ID=
S3_ACCESS_KEY_SECRET=

# MinIO (for local S3)
MINIO_ROOT_USER=admin
MINIO_ROOT_PASSWORD=admin123

# Feature Flags
ENABLE_FALLBACK=false
ENABLE_CACHE=false
EOF
        print_success "Created .env file"
    else
        print_warning ".env file already exists, skipping"
    fi
}

create_directories() {
    echo -e "${CYAN}Creating project directories...${NC}"
    
    directories=(
        "test/integration"
        "test/e2e"
        "test/kurtosis"
        "test/testdata"
        "monitoring/grafana/dashboards"
        "monitoring/grafana/datasources"
        "scripts"
    )
    
    for dir in "${directories[@]}"; do
        if [ ! -d "$dir" ]; then
            mkdir -p "$dir"
            print_success "Created $dir"
        else
            print_warning "$dir already exists"
        fi
    done
}

check_dependencies() {
    echo -e "${CYAN}Checking dependencies...${NC}"
    echo
    
    # Check required tools
    required_tools=("git" "go" "make")
    missing_tools=()
    
    for tool in "${required_tools[@]}"; do
        if ! check_command "$tool"; then
            missing_tools+=("$tool")
        fi
    done
    
    # Check optional tools
    echo
    echo -e "${CYAN}Checking optional tools...${NC}"
    
    optional_tools=("docker" "docker-compose" "golangci-lint" "kurtosis")
    
    for tool in "${optional_tools[@]}"; do
        check_command "$tool" || true
    done
    
    # Exit if required tools are missing
    if [ ${#missing_tools[@]} -ne 0 ]; then
        echo
        print_error "Missing required tools: ${missing_tools[*]}"
        echo "Please install the missing tools and run this script again."
        exit 1
    fi
}

install_tools() {
    echo
    echo -e "${CYAN}Would you like to install missing optional tools? (y/n)${NC}"
    read -r response
    
    if [[ "$response" =~ ^([yY][eE][sS]|[yY])$ ]]; then
        # Install golangci-lint if missing
        if ! command -v golangci-lint &> /dev/null; then
            install_golangci_lint
        fi
        
        # Check for docker-compose
        if ! command -v docker-compose &> /dev/null; then
            install_docker_compose
        fi
    fi
}

setup_ci_files() {
    echo -e "${CYAN}CI/CD files have been created:${NC}"
    echo
    echo "GitHub Actions Workflows:"
    print_success ".github/workflows/ci.yml - Main CI pipeline"
    print_success ".github/workflows/release.yml - Release automation"
    print_success ".github/workflows/codeql.yml - Security analysis"
    print_success ".github/workflows/dependency-review.yml - Dependency checking"
    print_success ".github/workflows/integration.yml - Integration tests"
    echo
    echo "Development Tools:"
    print_success "Makefile - Build and development commands"
    print_success "Dockerfile - Container image definition"
    print_success "docker-compose.yml - Local development environment"
    print_success ".golangci.yml - Linting configuration"
    echo
    echo "GitHub Templates:"
    print_success ".github/pull_request_template.md"
    print_success ".github/ISSUE_TEMPLATE/bug_report.md"
    print_success ".github/ISSUE_TEMPLATE/feature_request.md"
}

run_initial_checks() {
    echo
    echo -e "${CYAN}Running initial checks...${NC}"
    
    # Try to download dependencies
    if make mod-download 2>/dev/null; then
        print_success "Dependencies downloaded"
    else
        print_warning "Could not download dependencies (go.mod might be missing)"
    fi
    
    # Try to run tests
    if make test 2>/dev/null; then
        print_success "Tests passed"
    else
        print_warning "Tests failed or no tests found yet"
    fi
}

print_next_steps() {
    echo
    echo -e "${CYAN}=================================================${NC}"
    echo -e "${CYAN}    Setup Complete!${NC}"
    echo -e "${CYAN}=================================================${NC}"
    echo
    echo -e "${GREEN}Next steps:${NC}"
    echo
    echo "1. Review and customize the CI/CD configuration files"
    echo "2. Update the .env file with your Celestia configuration"
    echo "3. Test the setup:"
    echo "   ${CYAN}make test${NC}              # Run unit tests"
    echo "   ${CYAN}make lint${NC}              # Run linters"
    echo "   ${CYAN}make dev${NC}               # Start development server"
    echo "   ${CYAN}make docker-build${NC}      # Build Docker image"
    echo
    echo "4. Start local development environment:"
    echo "   ${CYAN}docker-compose up -d${NC}"
    echo
    echo "5. Commit the changes:"
    echo "   ${CYAN}git add .${NC}"
    echo "   ${CYAN}git commit -m \"Add comprehensive CI/CD setup\"${NC}"
    echo
    echo "For more information, see CI_CD_README.md"
}

# Main execution
main() {
    print_header
    check_dependencies
    install_tools
    create_directories
    setup_environment
    
    echo
    echo -e "${CYAN}Would you like to set up Git hooks? (y/n)${NC}"
    read -r response
    if [[ "$response" =~ ^([yY][eE][sS]|[yY])$ ]]; then
        setup_git_hooks
    fi
    
    echo
    setup_ci_files
    run_initial_checks
    print_next_steps
}

# Run main function
main
