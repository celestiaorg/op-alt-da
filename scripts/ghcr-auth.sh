#!/bin/bash

# GHCR Authentication Helper Script

set -e

# Colors
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
CYAN='\033[0;36m'
NC='\033[0m'

print_header() {
    echo -e "${CYAN}================================${NC}"
    echo -e "${CYAN}  GHCR Authentication Helper${NC}"
    echo -e "${CYAN}================================${NC}"
    echo
}

print_success() {
    echo -e "${GREEN}✓${NC} $1"
}

print_error() {
    echo -e "${RED}✗${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}⚠${NC} $1"
}

check_prerequisites() {
    if ! command -v docker &> /dev/null; then
        print_error "Docker is not installed"
        echo "Please install Docker first: https://docs.docker.com/get-docker/"
        exit 1
    fi
    
    if ! command -v gh &> /dev/null; then
        print_warning "GitHub CLI (gh) is not installed"
        echo "Install with: brew install gh (macOS) or see https://cli.github.com/"
        USE_GH_CLI=false
    else
        USE_GH_CLI=true
    fi
}

get_github_username() {
    if [ -n "$GITHUB_USERNAME" ]; then
        echo "$GITHUB_USERNAME"
    elif [ "$USE_GH_CLI" = true ]; then
        gh api user -q .login 2>/dev/null || echo ""
    else
        echo ""
    fi
}

authenticate_with_gh_cli() {
    echo -e "${CYAN}Authenticating with GitHub CLI...${NC}"
    
    if ! gh auth status &> /dev/null; then
        print_warning "Not logged in to GitHub CLI"
        echo "Running: gh auth login"
        gh auth login
    fi
    
    # Get the token from gh cli
    TOKEN=$(gh auth token)
    USERNAME=$(gh api user -q .login)
    
    echo "$TOKEN" | docker login ghcr.io -u "$USERNAME" --password-stdin
    
    if [ $? -eq 0 ]; then
        print_success "Successfully authenticated to GHCR using GitHub CLI"
        echo
        echo "You can now push images to: ghcr.io/$USERNAME/"
    else
        print_error "Authentication failed"
        exit 1
    fi
}

authenticate_with_pat() {
    echo -e "${CYAN}Authenticating with Personal Access Token...${NC}"
    echo
    
    # Get username
    read -p "Enter your GitHub username: " USERNAME
    
    echo
    echo "You need a Personal Access Token with 'write:packages' scope."
    echo "Create one at: https://github.com/settings/tokens/new"
    echo "Required scopes:"
    echo "  ✓ write:packages (to push images)"
    echo "  ✓ read:packages (to pull images)"
    echo "  ✓ delete:packages (optional, to delete images)"
    echo
    
    # Get token (hidden input)
    read -s -p "Enter your Personal Access Token: " TOKEN
    echo
    echo
    
    # Attempt login
    echo "$TOKEN" | docker login ghcr.io -u "$USERNAME" --password-stdin
    
    if [ $? -eq 0 ]; then
        print_success "Successfully authenticated to GHCR"
        echo
        echo "You can now push images to: ghcr.io/$USERNAME/"
        
        # Offer to save credentials
        echo
        read -p "Save credentials to environment file? (y/n): " SAVE_CREDS
        if [[ "$SAVE_CREDS" =~ ^([yY][eE][sS]|[yY])$ ]]; then
            save_credentials "$USERNAME" "$TOKEN"
        fi
    else
        print_error "Authentication failed"
        echo "Please check your username and token"
        exit 1
    fi
}

save_credentials() {
    local username=$1
    local token=$2
    
    cat >> .env.ghcr << EOF

# GitHub Container Registry Credentials
# WARNING: Do not commit this file to version control!
GITHUB_USERNAME=$username
GITHUB_TOKEN=$token
GHCR_NAMESPACE=ghcr.io/$username
EOF
    
    # Add to .gitignore if not already there
    if ! grep -q ".env.ghcr" .gitignore 2>/dev/null; then
        echo ".env.ghcr" >> .gitignore
    fi
    
    print_success "Credentials saved to .env.ghcr"
    print_warning "Added .env.ghcr to .gitignore - do not commit this file!"
}

test_authentication() {
    echo
    echo -e "${CYAN}Testing authentication...${NC}"
    
    # Try to pull a public image from ghcr to test
    if docker pull ghcr.io/actions/runner:latest &> /dev/null; then
        print_success "Successfully pulled test image"
    else
        print_warning "Could not pull test image (this is normal for private registries)"
    fi
    
    # Check login status
    if docker login ghcr.io --get-login &> /dev/null; then
        print_success "Docker is authenticated to ghcr.io"
    else
        print_error "Docker is not authenticated to ghcr.io"
    fi
}

show_usage() {
    echo
    echo -e "${CYAN}How to use GHCR:${NC}"
    echo
    echo "1. Pull public images:"
    echo "   docker pull ghcr.io/celestiaorg/op-alt-da:latest"
    echo
    echo "2. Push your images:"
    echo "   docker build -t ghcr.io/$USERNAME/op-alt-da:latest ."
    echo "   docker push ghcr.io/$USERNAME/op-alt-da:latest"
    echo
    echo "3. Use in docker-compose.yml:"
    echo "   image: ghcr.io/celestiaorg/op-alt-da:latest"
    echo
    echo "4. Use the Makefile:"
    echo "   make docker-build"
    echo "   make docker-push"
}

logout() {
    echo -e "${CYAN}Logging out from GHCR...${NC}"
    docker logout ghcr.io
    print_success "Logged out from ghcr.io"
}

main() {
    print_header
    
    if [ "$1" = "logout" ]; then
        logout
        exit 0
    fi
    
    check_prerequisites
    
    echo "Choose authentication method:"
    echo "1) GitHub CLI (recommended if installed)"
    echo "2) Personal Access Token"
    echo "3) Exit"
    echo
    
    read -p "Enter choice (1-3): " choice
    
    case $choice in
        1)
            if [ "$USE_GH_CLI" = true ]; then
                authenticate_with_gh_cli
            else
                print_error "GitHub CLI is not installed"
                echo "Falling back to PAT authentication..."
                authenticate_with_pat
            fi
            ;;
        2)
            authenticate_with_pat
            ;;
        3)
            echo "Exiting..."
            exit 0
            ;;
        *)
            print_error "Invalid choice"
            exit 1
            ;;
    esac
    
    test_authentication
    show_usage
}

# Run main function
main "$@"
